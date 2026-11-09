package vm

import (
	"fmt"
	"sort"

	"m31labs.dev/arbiter/bytecode"
	"m31labs.dev/arbiter/intern"
)

func (vm *VM) evalTableOp(instrs []byte, end, ip uint32, op bytecode.OpCode, flags uint8, arg uint16, dc DataContext) (uint32, bool) {
	switch op {
	case bytecode.OpLookup:
		vm.execLookup(instrs, arg, dc)
		return nextInstruction(ip), true

	case bytecode.OpTableField:
		row := vm.pop()
		fieldName := vm.strPool.Get(arg)
		if row.Typ == TypeNull {
			vm.err = nil
			vm.push(NullVal())
			vm.setErr(fmt.Errorf("cannot access field %q on null row", fieldName))
			return nextInstruction(ip), true
		}
		var loaded Value
		switch obj := row.Any.(type) {
		case map[string]any:
			val, exists := obj[fieldName]
			if !exists {
				vm.push(NullVal())
				return nextInstruction(ip), true
			}
			loaded = anyToValue(val, vm.strPool)
		case *tableRow:
			loaded = obj.field(fieldName, vm.strPool)
		case *elseRow:
			loaded = obj.field(fieldName)
		default:
			vm.push(NullVal())
			return nextInstruction(ip), true
		}
		if vm.profile == NumericInteger {
			loaded = vm.checkLoaded(loaded)
		}
		vm.push(loaded)
		return nextInstruction(ip), true

	default:
		return 0, false
	}
}

func (vm *VM) execLookup(instrs []byte, metaIdx uint16, dc DataContext) {
	if vm.rs == nil || int(metaIdx) >= len(vm.rs.LookupMetas) {
		vm.push(NullVal())
		return
	}
	meta := &vm.rs.LookupMetas[metaIdx]

	if int(meta.TableIdx) >= len(vm.rs.Tables) {
		vm.push(NullVal())
		return
	}
	table := &vm.rs.Tables[meta.TableIdx]

	sortColIdx := -1
	if meta.SortCol != "" {
		for j, colName := range table.Columns {
			if colName == meta.SortCol {
				sortColIdx = j
				break
			}
		}
	}

	// Save current locals so we can restore after evaluating where clauses.
	frame := vm.pushFrame()

	// The result is the first matching row, or with "order by" the first
	// matching row with the smallest (or largest) sort value, which is the row
	// a stable sort puts first. When every cell of the sort column is a number,
	// or every cell is a boolean, a linear scan finds that row without
	// allocating. Any other column keeps the historical stable sort.
	legacySort := sortColIdx >= 0 && !columnOrdered(table, sortColIdx)
	best := -1
	var matches []int
	for i, row := range table.Rows {
		if meta.WhereLen > 0 {
			// Bind column values as locals for the where clause.
			for j, colName := range table.Columns {
				if j < len(row) {
					vm.bindLocal(colName, vm.poolValueToValue(row[j]))
				}
			}

			// Evaluate the where clause above the caller's stack values.
			savedSP := vm.sp
			vm.evalCondition(instrs, meta.WhereOff, meta.WhereLen, dc)
			var result bool
			if vm.sp > savedSP {
				result = vm.pop().AsBool()
			}
			vm.sp = savedSP

			if vm.err != nil {
				vm.popFrame(frame)
				vm.push(NullVal())
				return
			}

			if !result {
				continue
			}
		}
		switch {
		case legacySort:
			matches = append(matches, i)
		case best < 0:
			best = i
		case sortColIdx >= 0:
			cmp := poolValueCompare(row[sortColIdx], table.Rows[best][sortColIdx])
			if (meta.SortDesc && cmp > 0) || (!meta.SortDesc && cmp < 0) {
				best = i
			}
		}
	}

	// Restore locals.
	vm.popFrame(frame)

	if legacySort && len(matches) > 0 {
		sortDesc := meta.SortDesc
		sort.SliceStable(matches, func(a, b int) bool {
			rowA := table.Rows[matches[a]]
			rowB := table.Rows[matches[b]]
			if sortColIdx >= len(rowA) || sortColIdx >= len(rowB) {
				return false
			}
			cmp := poolValueCompare(rowA[sortColIdx], rowB[sortColIdx])
			if sortDesc {
				return cmp > 0
			}
			return cmp < 0
		})
		best = matches[0]
	}

	if best >= 0 {
		vm.push(vm.tableRowValue(meta.TableIdx, best))
	} else if meta.ElseLen > 0 {
		vm.push(vm.evalElseRow(instrs, meta, dc))
	} else {
		vm.push(NullVal())
	}
}

// columnOrdered reports whether every row has column col and all its cells
// are numbers, or all are booleans, so poolValueCompare orders them totally.
func columnOrdered(table *bytecode.CompiledTable, col int) bool {
	var typ uint8
	for i, row := range table.Rows {
		if col >= len(row) {
			return false
		}
		cell := row[col]
		if cell.Typ != intern.TypeNumber && cell.Typ != intern.TypeBool {
			return false
		}
		if i == 0 {
			typ = cell.Typ
		} else if cell.Typ != typ {
			return false
		}
	}
	return true
}

func (vm *VM) evalElseRow(instrs []byte, meta *bytecode.LookupMeta, dc DataContext) Value {
	savedSP := vm.sp
	frame := vm.pushFrame()

	// Evaluate else expressions above the caller's stack values. There is one
	// expression per else key, pushed left to right.
	vm.evalCondition(instrs, meta.ElseOff, meta.ElseLen, dc)

	row := vm.nextElseRow(meta.ElseKeys)
	for i := len(meta.ElseKeys) - 1; i >= 0; i-- {
		if vm.sp > savedSP {
			row.vals[i] = localValue(vm.pop())
		}
	}

	vm.sp = savedSP
	vm.popFrame(frame)
	return ObjectVal(row)
}

// tableRow is a reference to one row of a compiled table. Lookups return it
// instead of building a map, so a lookup does not allocate. It is converted to
// a map only when a caller asks for the Go form of a value.
type tableRow struct {
	table *bytecode.CompiledTable
	row   int
}

// field returns a cell the way the historical map form did: null, booleans,
// numbers and strings are kept, other cell types read as null.
func (r *tableRow) field(name string, sp *StringPool) Value {
	if r.row < 0 || r.row >= len(r.table.Rows) {
		return NullVal()
	}
	cells := r.table.Rows[r.row]
	for j, col := range r.table.Columns {
		if col != name || j >= len(cells) {
			continue
		}
		cell := cells[j]
		switch cell.Typ {
		case intern.TypeBool:
			return BoolVal(cell.Bool)
		case intern.TypeNumber:
			return NumVal(cell.Num)
		case intern.TypeString:
			return StrVal(cell.Str)
		default:
			return NullVal()
		}
	}
	return NullVal()
}

func (r *tableRow) toMap(sp *StringPool) map[string]any {
	m := make(map[string]any, len(r.table.Columns))
	if r.row < 0 || r.row >= len(r.table.Rows) {
		return m
	}
	cells := r.table.Rows[r.row]
	for j, colName := range r.table.Columns {
		if j < len(cells) {
			m[colName] = poolValueToAnyStatic(cells[j], sp)
		}
	}
	return m
}

// tableRowValue returns a reference to a table row. The references are
// created once per table and reused.
func (vm *VM) tableRowValue(tableIdx uint16, row int) Value {
	if int(tableIdx) >= len(vm.rowRefs) {
		vm.rowRefs = append(vm.rowRefs, make([][]tableRow, int(tableIdx)+1-len(vm.rowRefs))...)
	}
	refs := vm.rowRefs[tableIdx]
	if refs == nil {
		table := &vm.rs.Tables[tableIdx]
		refs = make([]tableRow, len(table.Rows))
		for i := range refs {
			refs[i] = tableRow{table: table, row: i}
		}
		vm.rowRefs[tableIdx] = refs
	}
	if row < 0 || row >= len(refs) {
		return NullVal()
	}
	return ObjectVal(&refs[row])
}

// elseRow is the row a lookup's else clause builds. Else rows live in an arena
// that is reset when a new evaluation starts, so building one does not
// allocate in steady state. A value that holds an else row is valid until the
// evaluator starts its next evaluation.
type elseRow struct {
	keys []string
	vals []Value
}

func (r *elseRow) field(name string) Value {
	for i, key := range r.keys {
		if key == name && i < len(r.vals) {
			return r.vals[i]
		}
	}
	return NullVal()
}

func (r *elseRow) toMap(vm *VM) map[string]any {
	m := make(map[string]any, len(r.keys))
	for i, key := range r.keys {
		if i < len(r.vals) {
			m[key] = vm.valueToAny(r.vals[i])
		}
	}
	return m
}

func (vm *VM) nextElseRow(keys []string) *elseRow {
	if vm.elseUsed == len(vm.elseRows) {
		vm.elseRows = append(vm.elseRows, &elseRow{})
	}
	row := vm.elseRows[vm.elseUsed]
	vm.elseUsed++
	row.keys = keys
	if cap(row.vals) < len(keys) {
		row.vals = make([]Value, len(keys))
	}
	row.vals = row.vals[:len(keys)]
	clear(row.vals)
	return row
}

// resetElseRows starts a new evaluation: else rows built by the previous one
// may be reused, and their values no longer retain caller data.
func (vm *VM) resetElseRows() {
	for _, row := range vm.elseRows[:vm.elseUsed] {
		clear(row.vals)
	}
	vm.elseUsed = 0
}

// poolValueToAnyStatic converts a PoolValue to a Go any without needing a VM.
func poolValueToAnyStatic(pv intern.PoolValue, sp *StringPool) any {
	switch pv.Typ {
	case intern.TypeNull:
		return nil
	case intern.TypeBool:
		return pv.Bool
	case intern.TypeNumber:
		return pv.Num
	case intern.TypeString:
		return sp.Get(pv.Str)
	default:
		return nil
	}
}

// poolValueCompare compares two PoolValues for sorting.
// Returns -1, 0, or 1.
func poolValueCompare(a, b intern.PoolValue) int {
	if a.Typ == intern.TypeNumber && b.Typ == intern.TypeNumber {
		switch {
		case a.Num < b.Num:
			return -1
		case a.Num > b.Num:
			return 1
		default:
			return 0
		}
	}
	if a.Typ == intern.TypeBool && b.Typ == intern.TypeBool {
		if a.Bool == b.Bool {
			return 0
		}
		if !a.Bool {
			return -1
		}
		return 1
	}
	return 0
}
