// vm/value.go
package vm

import dec "github.com/odvcencio/arbiter/decimal"

// Type tags for Value.
const (
	TypeNull    uint8 = 0
	TypeBool    uint8 = 1
	TypeNumber  uint8 = 2
	TypeString  uint8 = 3
	TypeList    uint8 = 4
	TypeObject  uint8 = 5
	TypeDecimal uint8 = 6
)

// Value is the VM's stack value. Designed to avoid heap allocation.
type Value struct {
	Typ     uint8
	Num     float64
	Str     uint16 // constant pool string index
	Bool    bool
	ListIdx uint16 // index into constant pool lists
	ListLen uint16
	Any     any
}

func NullVal() Value                { return Value{Typ: TypeNull} }
func BoolVal(b bool) Value          { return Value{Typ: TypeBool, Bool: b} }
func NumVal(n float64) Value        { return Value{Typ: TypeNumber, Num: n} }
func DecimalVal(v any) Value        { return Value{Typ: TypeDecimal, Any: v} }
func StrVal(poolIdx uint16) Value   { return Value{Typ: TypeString, Str: poolIdx} }
func ListVal(idx, len uint16) Value { return Value{Typ: TypeList, ListIdx: idx, ListLen: len} }
func DynListVal(items any) Value    { return Value{Typ: TypeList, Any: items} }
func ObjectVal(obj any) Value       { return Value{Typ: TypeObject, Any: obj} }

func (v Value) IsNull() bool { return v.Typ == TypeNull }

func (v Value) Equal(other Value) bool {
	if v.Typ != other.Typ {
		return false
	}
	switch v.Typ {
	case TypeNull:
		return true
	case TypeBool:
		return v.Bool == other.Bool
	case TypeNumber:
		return v.Num == other.Num
	case TypeString:
		return v.Str == other.Str
	case TypeDecimal:
		left, lok := v.Any.(dec.Value)
		right, rok := other.Any.(dec.Value)
		return lok && rok && left.Equal(right)
	default:
		return false
	}
}

// AsBool returns the boolean value, or false for non-bool types.
func (v Value) AsBool() bool {
	if v.Typ == TypeBool {
		return v.Bool
	}
	return false
}
