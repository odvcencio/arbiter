package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"m31labs.dev/arbiter/bundle/wire"
	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/ir"
)

// Precompiled program format (all integers little-endian):
//
//	[4 bytes] magic "ARXP"
//	[1 byte]  format version
//	[4 bytes] metadata length, then metadata (JSON)
//	[4 bytes] rule bytecode length, then a wire bundle (0 length = no rules)
//	for each segment in metadata order:
//	  [4 bytes] length, then a wire bundle
//
// The output is deterministic: the same program always encodes to the same bytes.

var programMagic = [4]byte{'A', 'R', 'X', 'P'}

const programFormatVersion = 2

type programMeta struct {
	Rules          []Rule             `json:"rules"`
	FactSchemas    []ir.FactSchema    `json:"fact_schemas,omitempty"`
	OutcomeSchemas []ir.OutcomeSchema `json:"outcome_schemas,omitempty"`
	Segments       []segmentMeta      `json:"segments,omitempty"`
}

type segmentMeta struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// MarshalBinary encodes the program so a later process can load it with
// UnmarshalProgram without parsing any .arb source.
func (p *Program) MarshalBinary() ([]byte, error) {
	if p == nil {
		return nil, errors.New("nil program")
	}
	meta := programMeta{Rules: p.rules}
	if meta.Rules == nil {
		meta.Rules = []Rule{}
	}
	for _, name := range sortedKeys(p.factSchemas) {
		meta.FactSchemas = append(meta.FactSchemas, *p.factSchemas[name])
	}
	for _, name := range sortedKeys(p.outcomeSchemas) {
		meta.OutcomeSchemas = append(meta.OutcomeSchemas, *p.outcomeSchemas[name])
	}
	var segBlobs [][]byte
	if p.segments != nil {
		segs := p.segments.All()
		sort.Slice(segs, func(i, j int) bool { return segs[i].Name < segs[j].Name })
		for _, seg := range segs {
			if seg.Ruleset == nil {
				return nil, fmt.Errorf("segment %q has no compiled ruleset", seg.Name)
			}
			blob, err := wire.Marshal(seg.Ruleset)
			if err != nil {
				return nil, fmt.Errorf("encode segment %q: %w", seg.Name, err)
			}
			meta.Segments = append(meta.Segments, segmentMeta{Name: seg.Name, Source: seg.Source})
			segBlobs = append(segBlobs, blob)
		}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("encode metadata: %w", err)
	}
	var ruleBlob []byte
	if p.ruleset != nil {
		if ruleBlob, err = wire.Marshal(p.ruleset); err != nil {
			return nil, fmt.Errorf("encode rules: %w", err)
		}
	}

	var buf bytes.Buffer
	buf.Write(programMagic[:])
	buf.WriteByte(programFormatVersion)
	for _, section := range append([][]byte{metaJSON, ruleBlob}, segBlobs...) {
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(section)))
		buf.Write(n[:])
		buf.Write(section)
	}
	return buf.Bytes(), nil
}

// UnmarshalProgram decodes a program written by Program.MarshalBinary.
func UnmarshalProgram(data []byte) (*Program, error) {
	if len(data) < 5 || !bytes.Equal(data[:4], programMagic[:]) {
		return nil, errors.New("not a precompiled expert program (bad magic)")
	}
	if data[4] != programFormatVersion {
		return nil, fmt.Errorf("unsupported precompiled expert program version %d", data[4])
	}
	rest := data[5:]
	next := func() ([]byte, error) {
		if len(rest) < 4 {
			return nil, errors.New("truncated precompiled expert program")
		}
		n := binary.LittleEndian.Uint32(rest)
		rest = rest[4:]
		if uint64(n) > uint64(len(rest)) {
			return nil, errors.New("truncated precompiled expert program")
		}
		section := rest[:n]
		rest = rest[n:]
		return section, nil
	}

	metaJSON, err := next()
	if err != nil {
		return nil, err
	}
	var meta programMeta
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	ruleBlob, err := next()
	if err != nil {
		return nil, err
	}

	p := &Program{
		rules:          meta.Rules,
		ruleByName:     make(map[string]Rule, len(meta.Rules)),
		factSchemas:    make(map[string]*ir.FactSchema, len(meta.FactSchemas)),
		outcomeSchemas: make(map[string]*ir.OutcomeSchema, len(meta.OutcomeSchemas)),
	}
	if len(p.rules) == 0 {
		p.rules = nil
	}
	for _, rule := range meta.Rules {
		p.ruleByName[rule.Name] = rule
	}
	for i := range meta.FactSchemas {
		p.factSchemas[meta.FactSchemas[i].Name] = &meta.FactSchemas[i]
	}
	for i := range meta.OutcomeSchemas {
		p.outcomeSchemas[meta.OutcomeSchemas[i].Name] = &meta.OutcomeSchemas[i]
	}
	if len(ruleBlob) > 0 {
		if p.ruleset, err = wire.Unmarshal(ruleBlob); err != nil {
			return nil, fmt.Errorf("decode rules: %w", err)
		}
	}
	p.segments = govern.NewSegmentSet()
	for _, seg := range meta.Segments {
		blob, err := next()
		if err != nil {
			return nil, err
		}
		var rs *compiler.CompiledRuleset
		if rs, err = wire.Unmarshal(blob); err != nil {
			return nil, fmt.Errorf("decode segment %q: %w", seg.Name, err)
		}
		p.segments.Add(&govern.CompiledSegment{Name: seg.Name, Source: seg.Source, Ruleset: rs})
	}
	if len(rest) != 0 {
		return nil, errors.New("trailing bytes after precompiled expert program")
	}
	return p, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
