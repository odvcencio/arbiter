// vm/value_test.go
package vm

import (
	"testing"

	dec "github.com/odvcencio/arbiter/decimal"
)

func TestValueEqual(t *testing.T) {
	tests := []struct {
		a, b Value
		want bool
	}{
		{NumVal(42), NumVal(42), true},
		{NumVal(42), NumVal(43), false},
		{StrVal(5), StrVal(5), true}, // same pool index
		{StrVal(5), StrVal(6), false},
		{BoolVal(true), BoolVal(true), true},
		{BoolVal(true), BoolVal(false), false},
		{NullVal(), NullVal(), true},
		{NullVal(), NumVal(0), false},
		{DecimalVal(dec.MustParse("10.25", "USD")), DecimalVal(dec.MustParse("10.25", "USD")), true},
		{DecimalVal(dec.MustParse("10.25", "USD")), DecimalVal(dec.MustParse("10.25", "EUR")), false},
	}
	for i, tt := range tests {
		if got := tt.a.Equal(tt.b); got != tt.want {
			t.Errorf("test %d: %v.Equal(%v) = %v, want %v", i, tt.a, tt.b, got, tt.want)
		}
	}
}

func TestValueIsNull(t *testing.T) {
	if !NullVal().IsNull() {
		t.Error("NullVal should be null")
	}
	if NumVal(0).IsNull() {
		t.Error("NumVal(0) should not be null")
	}
}
