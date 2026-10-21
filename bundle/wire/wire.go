// Package bundle provides binary serialization and obfuscation for compiled
// Arbiter rulesets. Use this to ship pre-compiled bundles to browsers and edge
// runtimes without exposing .arb source, governance parameters, or rule internals.
package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"

	"m31labs.dev/arbiter/compiler"
	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/intern"
	"m31labs.dev/arbiter/ir"
)

// Magic bytes and version for the binary format.
var magic = [4]byte{'A', 'R', 'B', '1'}

// ObfuscateOptions controls what gets stripped or hashed in the serialized bundle.
type ObfuscateOptions struct {
	// HashRuleNames replaces rule names with SHA-256 truncated hashes.
	// The VM doesn't need human-readable rule names for evaluation.
	HashRuleNames bool

	// HashSegmentNames replaces segment names with hashes.
	HashSegmentNames bool

	// StripRolloutDetails zeros out rollout BPS, subject, and namespace fields.
	// The client receives a go/no-go decision from the server, not the rollout config.
	StripRolloutDetails bool

	// StripPrereqs removes prerequisite and exclusion arrays.
	// Only meaningful if governance is evaluated server-side.
	StripPrereqs bool

	// PreserveStrings lists string pool indices that must NOT be obfuscated
	// (e.g., action names and param keys the caller reads from results).
	// Computed automatically if nil.
	PreserveStrings map[uint16]bool
}

// Marshal serializes a CompiledRuleset to a binary blob.
func Marshal(rs *compiler.CompiledRuleset) ([]byte, error) {
	return MarshalObfuscated(rs, ObfuscateOptions{})
}

// MarshalObfuscated serializes a CompiledRuleset with obfuscation applied.
func MarshalObfuscated(rs *compiler.CompiledRuleset, opts ObfuscateOptions) ([]byte, error) {
	if rs == nil {
		return nil, fmt.Errorf("nil ruleset")
	}

	// Build the set of string indices we must preserve (action names, param keys).
	preserve := opts.PreserveStrings
	if preserve == nil {
		preserve = computePreservedStrings(rs)
	}

	var buf bytes.Buffer
	buf.Write(magic[:])

	// Constant pool: strings
	strs := rs.Constants.Strings()
	writeU32(&buf, uint32(len(strs)))
	for i, s := range strs {
		if shouldObfuscateString(uint16(i), rs, opts, preserve) {
			s = hashString(s)
		}
		writeString(&buf, s)
	}

	// Constant pool: numbers
	nums := rs.Constants.Numbers()
	writeU32(&buf, uint32(len(nums)))
	for _, n := range nums {
		writeF64(&buf, n)
	}

	// Constant pool: decimals
	decs := rs.Constants.Decimals()
	writeU32(&buf, uint32(len(decs)))
	for _, d := range decs {
		writeString(&buf, d.Text())
		writeString(&buf, d.Unit())
	}

	// Constant pool: lists
	lists := rs.Constants.Lists()
	writeU32(&buf, uint32(len(lists)))
	for _, pv := range lists {
		buf.WriteByte(pv.Typ)
		writeF64(&buf, pv.Num)
		writeU16(&buf, pv.Str)
		writeBool(&buf, pv.Bool)
		writeU16(&buf, pv.ListIdx)
		writeU16(&buf, pv.ListLen)
		writeU16(&buf, pv.Dec)
	}

	// Instructions
	writeU32(&buf, uint32(len(rs.Instructions)))
	buf.Write(rs.Instructions)

	// Rules
	writeU32(&buf, uint32(len(rs.Rules)))
	for _, r := range rs.Rules {
		rule := r
		if opts.StripRolloutDetails {
			rule.HasRollout = false
			rule.RolloutBps = 0
			rule.RolloutSubjectIdx = 0
			rule.RolloutNamespaceIdx = 0
			rule.HasRolloutSubject = false
			rule.HasRolloutNamespace = false
		}
		writeRuleHeader(&buf, rule)
	}

	// Actions
	writeU32(&buf, uint32(len(rs.Actions)))
	for _, a := range rs.Actions {
		writeU16(&buf, a.NameIdx)
		writeU32(&buf, uint32(len(a.Params)))
		for _, p := range a.Params {
			writeU16(&buf, p.KeyIdx)
			writeU32(&buf, p.ValueOff)
			writeU32(&buf, p.ValueLen)
		}
	}

	// Tags
	writeU32(&buf, uint32(len(rs.Tags)))
	for _, idx := range rs.Tags {
		writeU16(&buf, idx)
	}

	// Prereqs
	if opts.StripPrereqs {
		writeU32(&buf, 0)
		writeU32(&buf, 0)
	} else {
		writeU32(&buf, uint32(len(rs.Prereqs)))
		for _, idx := range rs.Prereqs {
			writeU16(&buf, idx)
		}
		writeU32(&buf, uint32(len(rs.Excludes)))
		for _, idx := range rs.Excludes {
			writeU16(&buf, idx)
		}
	}

	// Tables
	writeU32(&buf, uint32(len(rs.Tables)))
	for _, tbl := range rs.Tables {
		writeString(&buf, tbl.Name)
		writeU16(&buf, uint16(len(tbl.Columns)))
		for _, col := range tbl.Columns {
			writeString(&buf, col)
		}
		writeU32(&buf, uint32(len(tbl.Rows)))
		for _, row := range tbl.Rows {
			for _, pv := range row {
				buf.WriteByte(pv.Typ)
				writeF64(&buf, pv.Num)
				writeU16(&buf, pv.Str)
				writeBool(&buf, pv.Bool)
				writeU16(&buf, pv.ListIdx)
				writeU16(&buf, pv.ListLen)
				writeU16(&buf, pv.Dec)
			}
		}
	}

	// LookupMetas
	writeU32(&buf, uint32(len(rs.LookupMetas)))
	for _, lm := range rs.LookupMetas {
		writeU16(&buf, lm.TableIdx)
		writeU32(&buf, lm.WhereOff)
		writeU32(&buf, lm.WhereLen)
		writeString(&buf, lm.SortCol)
		writeBool(&buf, lm.SortDesc)
		writeU32(&buf, lm.ElseOff)
		writeU32(&buf, lm.ElseLen)
		writeU16(&buf, uint16(len(lm.ElseKeys)))
		for _, k := range lm.ElseKeys {
			writeString(&buf, k)
		}
	}

	return buf.Bytes(), nil
}

// Unmarshal deserializes a binary blob back into a CompiledRuleset.
func Unmarshal(data []byte) (*compiler.CompiledRuleset, error) {
	d := newBundleDecoder(data)

	var m [4]byte
	if _, err := io.ReadFull(d.r, m[:]); err != nil {
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if m != magic {
		return nil, fmt.Errorf("invalid bundle magic: %x", m)
	}

	// Strings
	strCount, err := d.readCount(4)
	if err != nil {
		return nil, fmt.Errorf("read string count: %w", err)
	}
	pool := intern.NewPool()
	for range strCount {
		s, err := d.readString()
		if err != nil {
			return nil, fmt.Errorf("read string: %w", err)
		}
		pool.String(s)
	}
	if err := pool.Err(); err != nil {
		return nil, fmt.Errorf("unmarshal strings: %w", err)
	}

	// Numbers
	numCount, err := d.readCount(8)
	if err != nil {
		return nil, fmt.Errorf("read number count: %w", err)
	}
	for range numCount {
		n, err := d.readF64()
		if err != nil {
			return nil, fmt.Errorf("read number: %w", err)
		}
		pool.Number(n)
	}
	if err := pool.Err(); err != nil {
		return nil, fmt.Errorf("unmarshal numbers: %w", err)
	}

	// Decimals
	decCount, err := d.readCount(8)
	if err != nil {
		return nil, fmt.Errorf("read decimal count: %w", err)
	}
	for range decCount {
		text, err := d.readString()
		if err != nil {
			return nil, fmt.Errorf("read decimal text: %w", err)
		}
		unit, err := d.readString()
		if err != nil {
			return nil, fmt.Errorf("read decimal unit: %w", err)
		}
		v, err := dec.Parse(text, unit)
		if err != nil {
			return nil, fmt.Errorf("unmarshal decimal: %w", err)
		}
		pool.Decimal(v)
	}
	if err := pool.Err(); err != nil {
		return nil, fmt.Errorf("unmarshal decimals: %w", err)
	}

	// Lists
	listCount, err := d.readCount(poolValueSerializedSize)
	if err != nil {
		return nil, fmt.Errorf("read list count: %w", err)
	}
	listItems := make([]intern.PoolValue, listCount)
	for i := range listCount {
		listItems[i], err = d.readPoolValue()
		if err != nil {
			return nil, fmt.Errorf("read list item: %w", err)
		}
	}
	if len(listItems) > 0 {
		pool.RestoreLists(listItems)
	}

	// Instructions
	instrLen, err := d.readCount(1)
	if err != nil {
		return nil, fmt.Errorf("read instruction length: %w", err)
	}
	instructions := make([]byte, instrLen)
	if _, err := io.ReadFull(d.r, instructions); err != nil {
		return nil, fmt.Errorf("read instructions: %w", err)
	}

	// Rules
	ruleCount, err := d.readCount(ruleHeaderSerializedSize)
	if err != nil {
		return nil, fmt.Errorf("read rule count: %w", err)
	}
	rules := make([]compiler.RuleHeader, ruleCount)
	for i := range ruleCount {
		rules[i], err = d.readRuleHeader()
		if err != nil {
			return nil, fmt.Errorf("read rule header: %w", err)
		}
	}

	// Actions
	actionCount, err := d.readCount(6)
	if err != nil {
		return nil, fmt.Errorf("read action count: %w", err)
	}
	actions := make([]compiler.ActionEntry, actionCount)
	for i := range actionCount {
		actions[i].NameIdx, err = d.readU16()
		if err != nil {
			return nil, fmt.Errorf("read action name index: %w", err)
		}
		paramCount, err := d.readCount(actionParamSerializedSize)
		if err != nil {
			return nil, fmt.Errorf("read action param count: %w", err)
		}
		actions[i].Params = make([]compiler.ActionParam, paramCount)
		for j := range paramCount {
			keyIdx, err := d.readU16()
			if err != nil {
				return nil, fmt.Errorf("read action param key: %w", err)
			}
			valueOff, err := d.readU32()
			if err != nil {
				return nil, fmt.Errorf("read action param offset: %w", err)
			}
			valueLen, err := d.readU32()
			if err != nil {
				return nil, fmt.Errorf("read action param length: %w", err)
			}
			actions[i].Params[j] = compiler.ActionParam{
				KeyIdx:   keyIdx,
				ValueOff: valueOff,
				ValueLen: valueLen,
			}
		}
	}

	// Tags
	tagCount, err := d.readCount(2)
	if err != nil {
		return nil, fmt.Errorf("read tag count: %w", err)
	}
	tags := make([]uint16, tagCount)
	for i := range tagCount {
		tags[i], err = d.readU16()
		if err != nil {
			return nil, fmt.Errorf("read tag value: %w", err)
		}
	}

	// Prereqs / Excludes
	prereqCount, err := d.readCount(2)
	if err != nil {
		return nil, fmt.Errorf("read prereq count: %w", err)
	}
	prereqs := make([]uint16, prereqCount)
	for i := range prereqCount {
		prereqs[i], err = d.readU16()
		if err != nil {
			return nil, fmt.Errorf("read prereq value: %w", err)
		}
	}
	excludeCount, err := d.readCount(2)
	if err != nil {
		return nil, fmt.Errorf("read exclude count: %w", err)
	}
	excludes := make([]uint16, excludeCount)
	for i := range excludeCount {
		excludes[i], err = d.readU16()
		if err != nil {
			return nil, fmt.Errorf("read exclude value: %w", err)
		}
	}

	// Tables
	tableCount, err := d.readCount(10)
	if err != nil {
		return nil, fmt.Errorf("read table count: %w", err)
	}
	tables := make([]compiler.CompiledTable, tableCount)
	for i := range tableCount {
		tbl := compiler.CompiledTable{}
		tbl.Name, err = d.readString()
		if err != nil {
			return nil, fmt.Errorf("read table name: %w", err)
		}
		colCount, err := d.readU16()
		if err != nil {
			return nil, fmt.Errorf("read table column count: %w", err)
		}
		if err := d.ensureAllocCount(uint32(colCount), 4); err != nil {
			return nil, fmt.Errorf("read table columns: %w", err)
		}
		tbl.Columns = make([]string, colCount)
		for j := range colCount {
			tbl.Columns[j], err = d.readString()
			if err != nil {
				return nil, fmt.Errorf("read table column: %w", err)
			}
		}
		rowCount, err := d.readCount(max(1, int(colCount)*poolValueSerializedSize))
		if err != nil {
			return nil, fmt.Errorf("read table row count: %w", err)
		}
		tbl.Rows = make([][]intern.PoolValue, rowCount)
		for j := range rowCount {
			tbl.Rows[j] = make([]intern.PoolValue, colCount)
			for k := range colCount {
				tbl.Rows[j][k], err = d.readPoolValue()
				if err != nil {
					return nil, fmt.Errorf("read table row value: %w", err)
				}
			}
		}
		tables[i] = tbl
	}

	// LookupMetas
	metaCount, err := d.readCount(25)
	if err != nil {
		return nil, fmt.Errorf("read lookup meta count: %w", err)
	}
	lookupMetas := make([]compiler.LookupMeta, metaCount)
	for i := range metaCount {
		lm := compiler.LookupMeta{}
		lm.TableIdx, err = d.readU16()
		if err != nil {
			return nil, fmt.Errorf("read lookup meta table index: %w", err)
		}
		lm.WhereOff, err = d.readU32()
		if err != nil {
			return nil, fmt.Errorf("read lookup meta where offset: %w", err)
		}
		lm.WhereLen, err = d.readU32()
		if err != nil {
			return nil, fmt.Errorf("read lookup meta where length: %w", err)
		}
		lm.SortCol, err = d.readString()
		if err != nil {
			return nil, fmt.Errorf("read lookup meta sort column: %w", err)
		}
		lm.SortDesc, err = d.readBool()
		if err != nil {
			return nil, fmt.Errorf("read lookup meta sort direction: %w", err)
		}
		lm.ElseOff, err = d.readU32()
		if err != nil {
			return nil, fmt.Errorf("read lookup meta else offset: %w", err)
		}
		lm.ElseLen, err = d.readU32()
		if err != nil {
			return nil, fmt.Errorf("read lookup meta else length: %w", err)
		}
		keyCount, err := d.readU16()
		if err != nil {
			return nil, fmt.Errorf("read lookup meta key count: %w", err)
		}
		if err := d.ensureAllocCount(uint32(keyCount), 4); err != nil {
			return nil, fmt.Errorf("read lookup meta keys: %w", err)
		}
		lm.ElseKeys = make([]string, keyCount)
		for j := range keyCount {
			lm.ElseKeys[j], err = d.readString()
			if err != nil {
				return nil, fmt.Errorf("read lookup meta key: %w", err)
			}
		}
		lookupMetas[i] = lm
	}
	if d.r.Len() != 0 {
		return nil, fmt.Errorf("bundle has %d trailing bytes", d.r.Len())
	}

	return &compiler.CompiledRuleset{
		Constants:    pool,
		Instructions: instructions,
		Rules:        rules,
		Actions:      actions,
		Tags:         tags,
		Prereqs:      prereqs,
		Excludes:     excludes,
		Tables:       tables,
		LookupMetas:  lookupMetas,
	}, nil
}

// computePreservedStrings finds string indices that must stay readable:
// action names and param keys (the caller reads these from results).
func computePreservedStrings(rs *compiler.CompiledRuleset) map[uint16]bool {
	keep := make(map[uint16]bool)
	for _, a := range rs.Actions {
		keep[a.NameIdx] = true
		for _, p := range a.Params {
			keep[p.KeyIdx] = true
		}
	}
	return keep
}

func shouldObfuscateString(idx uint16, rs *compiler.CompiledRuleset, opts ObfuscateOptions, preserve map[uint16]bool) bool {
	if preserve[idx] {
		return false
	}
	if opts.HashRuleNames {
		for _, r := range rs.Rules {
			if r.NameIdx == idx {
				return true
			}
		}
	}
	if opts.HashSegmentNames {
		for _, r := range rs.Rules {
			if r.HasSegment && r.SegmentNameIdx == idx {
				return true
			}
		}
	}
	return false
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// --- Binary encoding helpers ---

func writeU16(w *bytes.Buffer, v uint16)  { binary.Write(w, binary.LittleEndian, v) }
func writeU32(w *bytes.Buffer, v uint32)  { binary.Write(w, binary.LittleEndian, v) }
func writeI64(w *bytes.Buffer, v int64)   { binary.Write(w, binary.LittleEndian, v) }
func writeF64(w *bytes.Buffer, v float64) { binary.Write(w, binary.LittleEndian, v) }
func writeBool(w *bytes.Buffer, v bool) {
	if v {
		w.WriteByte(1)
	} else {
		w.WriteByte(0)
	}
}
func writeKillSwitchState(w *bytes.Buffer, v ir.KillSwitchState) {
	switch v {
	case ir.KillSwitchOn:
		w.WriteByte(1)
	case ir.KillSwitchOff:
		w.WriteByte(2)
	default:
		w.WriteByte(0)
	}
}
func writeString(w *bytes.Buffer, s string) {
	writeU32(w, uint32(len(s)))
	w.WriteString(s)
}
func writeRuleHeader(w *bytes.Buffer, r compiler.RuleHeader) {
	writeU16(w, r.NameIdx)
	binary.Write(w, binary.LittleEndian, r.Priority)
	writeU32(w, r.ConditionOff)
	writeU32(w, r.ConditionLen)
	writeU16(w, r.ActionIdx)
	writeU16(w, r.FallbackIdx)
	writeKillSwitchState(w, r.KillSwitch)
	writeBool(w, r.HasActiveFrom)
	writeI64(w, r.ActiveFromUnixNano)
	writeBool(w, r.HasActiveUntil)
	writeI64(w, r.ActiveUntilUnixNano)
	writeBool(w, r.HasRollout)
	writeU16(w, r.RolloutBps)
	writeU16(w, r.RolloutSubjectIdx)
	writeU16(w, r.RolloutNamespaceIdx)
	writeBool(w, r.HasRolloutSubject)
	writeBool(w, r.HasRolloutNamespace)
	writeU16(w, r.PrereqOff)
	writeU16(w, r.PrereqLen)
	writeU16(w, r.TagOff)
	writeU16(w, r.TagLen)
	writeU16(w, r.ExcludeOff)
	writeU16(w, r.ExcludeLen)
	writeU16(w, r.SegmentNameIdx)
	writeBool(w, r.HasSegment)
}

const (
	poolValueSerializedSize   = 18
	ruleHeaderSerializedSize  = 59
	actionParamSerializedSize = 10
)

type bundleDecoder struct {
	r *bytes.Reader
}

func newBundleDecoder(data []byte) *bundleDecoder {
	return &bundleDecoder{r: bytes.NewReader(data)}
}

func (d *bundleDecoder) ensureBytes(n uint32) error {
	if n > uint32(d.r.Len()) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (d *bundleDecoder) ensureAllocCount(count uint32, minItemSize int) error {
	if count == 0 {
		return nil
	}
	if minItemSize <= 0 {
		minItemSize = 1
	}
	maxCount := uint64(d.r.Len()) / uint64(minItemSize)
	if maxCount == 0 && d.r.Len() > 0 {
		maxCount = 1
	}
	if uint64(count) > maxCount {
		return fmt.Errorf("declared count %d exceeds remaining bundle capacity", count)
	}
	return nil
}

func (d *bundleDecoder) readU8() (uint8, error) {
	var v uint8
	if err := binary.Read(d.r, binary.LittleEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func (d *bundleDecoder) readI32() (int32, error) {
	var v int32
	if err := binary.Read(d.r, binary.LittleEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func (d *bundleDecoder) readI64() (int64, error) {
	var v int64
	if err := binary.Read(d.r, binary.LittleEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func (d *bundleDecoder) readU16() (uint16, error) {
	var v uint16
	if err := binary.Read(d.r, binary.LittleEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func (d *bundleDecoder) readU32() (uint32, error) {
	var v uint32
	if err := binary.Read(d.r, binary.LittleEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func (d *bundleDecoder) readCount(minItemSize int) (uint32, error) {
	count, err := d.readU32()
	if err != nil {
		return 0, err
	}
	if err := d.ensureAllocCount(count, minItemSize); err != nil {
		return 0, err
	}
	return count, nil
}

func (d *bundleDecoder) readF64() (float64, error) {
	var v float64
	if err := binary.Read(d.r, binary.LittleEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func (d *bundleDecoder) readBool() (bool, error) {
	v, err := d.readU8()
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

func (d *bundleDecoder) readKillSwitchState() (ir.KillSwitchState, error) {
	v, err := d.readU8()
	if err != nil {
		return ir.KillSwitchUnset, err
	}
	switch v {
	case 1:
		return ir.KillSwitchOn, nil
	case 2:
		return ir.KillSwitchOff, nil
	default:
		return ir.KillSwitchUnset, nil
	}
}

func (d *bundleDecoder) readString() (string, error) {
	n, err := d.readU32()
	if err != nil {
		return "", err
	}
	if err := d.ensureBytes(n); err != nil {
		return "", err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func (d *bundleDecoder) readPoolValue() (intern.PoolValue, error) {
	typ, err := d.readU8()
	if err != nil {
		return intern.PoolValue{}, err
	}
	num, err := d.readF64()
	if err != nil {
		return intern.PoolValue{}, err
	}
	str, err := d.readU16()
	if err != nil {
		return intern.PoolValue{}, err
	}
	boolean, err := d.readBool()
	if err != nil {
		return intern.PoolValue{}, err
	}
	listIdx, err := d.readU16()
	if err != nil {
		return intern.PoolValue{}, err
	}
	listLen, err := d.readU16()
	if err != nil {
		return intern.PoolValue{}, err
	}
	decimal, err := d.readU16()
	if err != nil {
		return intern.PoolValue{}, err
	}
	return intern.PoolValue{
		Typ:     typ,
		Num:     num,
		Str:     str,
		Bool:    boolean,
		ListIdx: listIdx,
		ListLen: listLen,
		Dec:     decimal,
	}, nil
}

func (d *bundleDecoder) readRuleHeader() (compiler.RuleHeader, error) {
	nameIdx, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	priority, err := d.readI32()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	conditionOff, err := d.readU32()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	conditionLen, err := d.readU32()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	actionIdx, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	fallbackIdx, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	killSwitch, err := d.readKillSwitchState()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	hasActiveFrom, err := d.readBool()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	activeFromUnixNano, err := d.readI64()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	hasActiveUntil, err := d.readBool()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	activeUntilUnixNano, err := d.readI64()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	hasRollout, err := d.readBool()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	rolloutBps, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	rolloutSubjectIdx, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	rolloutNamespaceIdx, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	hasRolloutSubject, err := d.readBool()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	hasRolloutNamespace, err := d.readBool()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	prereqOff, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	prereqLen, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	tagOff, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	tagLen, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	excludeOff, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	excludeLen, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	segmentNameIdx, err := d.readU16()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	hasSegment, err := d.readBool()
	if err != nil {
		return compiler.RuleHeader{}, err
	}
	return compiler.RuleHeader{
		NameIdx:             nameIdx,
		Priority:            priority,
		ConditionOff:        conditionOff,
		ConditionLen:        conditionLen,
		ActionIdx:           actionIdx,
		FallbackIdx:         fallbackIdx,
		KillSwitch:          killSwitch,
		HasActiveFrom:       hasActiveFrom,
		ActiveFromUnixNano:  activeFromUnixNano,
		HasActiveUntil:      hasActiveUntil,
		ActiveUntilUnixNano: activeUntilUnixNano,
		HasRollout:          hasRollout,
		RolloutBps:          rolloutBps,
		RolloutSubjectIdx:   rolloutSubjectIdx,
		RolloutNamespaceIdx: rolloutNamespaceIdx,
		HasRolloutSubject:   hasRolloutSubject,
		HasRolloutNamespace: hasRolloutNamespace,
		PrereqOff:           prereqOff,
		PrereqLen:           prereqLen,
		TagOff:              tagOff,
		TagLen:              tagLen,
		ExcludeOff:          excludeOff,
		ExcludeLen:          excludeLen,
		SegmentNameIdx:      segmentNameIdx,
		HasSegment:          hasSegment,
	}, nil
}
