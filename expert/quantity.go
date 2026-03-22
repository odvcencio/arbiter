package expert

import (
	dec "github.com/odvcencio/arbiter/decimal"
	"github.com/odvcencio/arbiter/units"
)

// Q constructs one runtime quantity value for schema-aware fact assertions.
func Q(value float64, unit string) units.Quantity {
	return units.Quantity{Value: value, Unit: unit}
}

// D constructs one runtime decimal value for schema-aware fact assertions.
func D(value, unit string) dec.Value {
	return dec.MustParse(value, unit)
}
