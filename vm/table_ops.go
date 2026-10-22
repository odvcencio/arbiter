package vm

import (
	"fmt"
	"sort"

	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/intern"
)

func (vm *VM) evalTableOp(instrs []byte, end, ip uint32, op compiler.OpCode, flags uint8, arg uint16, dc DataContext) (uint32, bool) {
	switch op {
	case compiler.OpLookup:
		vm.execLookup(instrs, arg, dc)
		return nextInstruction(ip), true

	case compiler.OpTableField:
		row := vm.pop()
		fieldName := vm.strPool.Get(arg)
		if row.Typ == TypeNull {
			vm.err = nil
			vm.push(NullVal())
			vm.setErr(fmt.Errorf("cannot access field %q on null row", fieldName))
			return nextInstruction(ip), true
		}
		if m, ok := row.Any.(map[string]any); ok {
			val, exists := m[fieldName]
			if !exists {
				vm.push(NullVal())
			} else {
				loaded := anyToValue(val, vm.strPool)
				if vm.profile == NumericInteger {
					loaded = vm.checkLoaded(loaded)
				}
				vm.push(loaded)
			}
		} else {
			vm.push(NullVal())
		}
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

	// Save current locals so we can restore after evaluating where clauses.
	savedLocals := cloneLocals(vm.locals)

	var matches []int
	for i, row := range table.Rows {
		if meta.WhereLen > 0 {
			// Bind column values as locals for the where clause.
			if vm.locals == nil {
				vm.locals = make(map[string]any)
			}
			for j, colName := range table.Columns {
				if j < len(row) {
					vm.locals[colName] = vm.poolValueToAny(row[j])
				}
			}

			// Evaluate the where clause.
			savedSP := vm.sp
			vm.sp = 0
			vm.evalCondition(instrs, meta.WhereOff, meta.WhereLen, dc)
			var result bool
			if vm.sp > 0 {
				result = vm.pop().AsBool()
			}
			vm.sp = savedSP

			if vm.err != nil {
				vm.locals = savedLocals
				vm.push(NullVal())
				return
			}

			if !result {
				continue
			}
		}
		matches = append(matches, i)
	}

	// Restore locals.
	vm.locals = savedLocals

	// Sort if needed.
	if meta.SortCol != "" && len(matches) > 1 {
		sortColIdx := -1
		for j, colName := range table.Columns {
			if colName == meta.SortCol {
				sortColIdx = j
				break
			}
		}
		if sortColIdx >= 0 {
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
		}
	}

	if len(matches) > 0 {
		vm.push(tableRowToValue(table, matches[0], vm.strPool))
	} else if meta.ElseLen > 0 {
		vm.push(vm.evalElseRow(instrs, meta, dc))
	} else {
		vm.push(NullVal())
	}
}

func (vm *VM) evalElseRow(instrs []byte, meta *compiler.LookupMeta, dc DataContext) Value {
	savedSP := vm.sp
	savedLocals := cloneLocals(vm.locals)
	vm.sp = 0

	// Evaluate else expressions. There is one expression per else key.
	vm.evalCondition(instrs, meta.ElseOff, meta.ElseLen, dc)

	// Pop values in reverse order (they were pushed left-to-right).
	nKeys := len(meta.ElseKeys)
	vals := make([]Value, nKeys)
	for i := nKeys - 1; i >= 0; i-- {
		if vm.sp > 0 {
			vals[i] = vm.pop()
		}
	}

	vm.sp = savedSP
	vm.locals = savedLocals

	row := make(map[string]any, nKeys)
	for i, key := range meta.ElseKeys {
		row[key] = vm.valueToAny(vals[i])
	}
	return ObjectVal(row)
}

// tableRowToValue converts a compiled table row into an ObjectVal (map[string]any).
func tableRowToValue(table *compiler.CompiledTable, rowIdx int, sp *StringPool) Value {
	if rowIdx < 0 || rowIdx >= len(table.Rows) {
		return NullVal()
	}
	row := table.Rows[rowIdx]
	m := make(map[string]any, len(table.Columns))
	for j, colName := range table.Columns {
		if j < len(row) {
			m[colName] = poolValueToAnyStatic(row[j], sp)
		}
	}
	return ObjectVal(m)
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
