package units

import (
	"fmt"
	"sort"
)

// Quantity is one numeric value paired with a unit symbol.
type Quantity struct {
	Value float64
	Unit  string
}

// Entry is one built-in unit table entry.
type Entry struct {
	Symbol    string
	Dimension string
	ToBase    float64
	Offset    float64
}

var entries = []Entry{
	{Symbol: "K", Dimension: "temperature", ToBase: 1, Offset: 0},
	{Symbol: "C", Dimension: "temperature", ToBase: 1, Offset: 273.15},
	{Symbol: "F", Dimension: "temperature", ToBase: 5.0 / 9.0, Offset: 255.3722222222222},

	{Symbol: "mm", Dimension: "length", ToBase: 0.001},
	{Symbol: "cm", Dimension: "length", ToBase: 0.01},
	{Symbol: "m", Dimension: "length", ToBase: 1},
	{Symbol: "km", Dimension: "length", ToBase: 1000},
	{Symbol: "in", Dimension: "length", ToBase: 0.0254},
	{Symbol: "ft", Dimension: "length", ToBase: 0.3048},
	{Symbol: "yd", Dimension: "length", ToBase: 0.9144},
	{Symbol: "mi", Dimension: "length", ToBase: 1609.344},

	{Symbol: "mg", Dimension: "mass", ToBase: 0.000001},
	{Symbol: "g", Dimension: "mass", ToBase: 0.001},
	{Symbol: "kg", Dimension: "mass", ToBase: 1},
	{Symbol: "lb", Dimension: "mass", ToBase: 0.45359237},
	{Symbol: "oz", Dimension: "mass", ToBase: 0.028349523125},

	{Symbol: "ms", Dimension: "time", ToBase: 0.001},
	{Symbol: "s", Dimension: "time", ToBase: 1},
	{Symbol: "min", Dimension: "time", ToBase: 60},
	{Symbol: "hr", Dimension: "time", ToBase: 3600},
	{Symbol: "d", Dimension: "time", ToBase: 86400},

	{Symbol: "mL", Dimension: "volume", ToBase: 0.001},
	{Symbol: "L", Dimension: "volume", ToBase: 1},
	{Symbol: "gal", Dimension: "volume", ToBase: 3.785411784},
	{Symbol: "fl_oz", Dimension: "volume", ToBase: 0.0295735295625},

	{Symbol: "Pa", Dimension: "pressure", ToBase: 1},
	{Symbol: "hPa", Dimension: "pressure", ToBase: 100},
	{Symbol: "kPa", Dimension: "pressure", ToBase: 1000},
	{Symbol: "bar", Dimension: "pressure", ToBase: 100000},
	{Symbol: "psi", Dimension: "pressure", ToBase: 6894.757293168361},
	{Symbol: "atm", Dimension: "pressure", ToBase: 101325},

	{Symbol: "pct", Dimension: "percentage", ToBase: 1},
	{Symbol: "%", Dimension: "percentage", ToBase: 1},

	{Symbol: "ppm", Dimension: "concentration", ToBase: 1},
	{Symbol: "ppb", Dimension: "concentration", ToBase: 0.001},

	{Symbol: "mm2", Dimension: "area", ToBase: 0.000001},
	{Symbol: "cm2", Dimension: "area", ToBase: 0.0001},
	{Symbol: "m2", Dimension: "area", ToBase: 1},
	{Symbol: "km2", Dimension: "area", ToBase: 1000000},
	{Symbol: "ha", Dimension: "area", ToBase: 10000},
	{Symbol: "acre", Dimension: "area", ToBase: 4046.8564224},

	{Symbol: "m/s", Dimension: "speed", ToBase: 1},
	{Symbol: "km/h", Dimension: "speed", ToBase: 1000.0 / 3600.0},
	{Symbol: "mph", Dimension: "speed", ToBase: 1609.344 / 3600.0},
	{Symbol: "kn", Dimension: "speed", ToBase: 1852.0 / 3600.0},

	{Symbol: "mA", Dimension: "electric_current", ToBase: 0.001},
	{Symbol: "A", Dimension: "electric_current", ToBase: 1},

	{Symbol: "mV", Dimension: "voltage", ToBase: 0.001},
	{Symbol: "V", Dimension: "voltage", ToBase: 1},
	{Symbol: "kV", Dimension: "voltage", ToBase: 1000},

	{Symbol: "W", Dimension: "power", ToBase: 1},
	{Symbol: "kW", Dimension: "power", ToBase: 1000},
	{Symbol: "MW", Dimension: "power", ToBase: 1000000},
	{Symbol: "hp", Dimension: "power", ToBase: 745.6998715822702},

	{Symbol: "J", Dimension: "energy", ToBase: 1},
	{Symbol: "kJ", Dimension: "energy", ToBase: 1000},
	{Symbol: "kWh", Dimension: "energy", ToBase: 3600000},
	{Symbol: "cal", Dimension: "energy", ToBase: 4.184},
	{Symbol: "kcal", Dimension: "energy", ToBase: 4184},

	{Symbol: "Hz", Dimension: "frequency", ToBase: 1},
	{Symbol: "kHz", Dimension: "frequency", ToBase: 1000},
	{Symbol: "MHz", Dimension: "frequency", ToBase: 1000000},
	{Symbol: "GHz", Dimension: "frequency", ToBase: 1000000000},

	{Symbol: "B", Dimension: "data", ToBase: 1},
	{Symbol: "KB", Dimension: "data", ToBase: 1000},
	{Symbol: "MB", Dimension: "data", ToBase: 1000000},
	{Symbol: "GB", Dimension: "data", ToBase: 1000000000},
	{Symbol: "TB", Dimension: "data", ToBase: 1000000000000},

	{Symbol: "L/min", Dimension: "flow", ToBase: 1},
	{Symbol: "L/hr", Dimension: "flow", ToBase: 1.0 / 60.0},
	{Symbol: "gal/min", Dimension: "flow", ToBase: 3.785411784},
	{Symbol: "m3/s", Dimension: "flow", ToBase: 60000},

	{Symbol: "USD", Dimension: "currency", ToBase: 1},
	{Symbol: "EUR", Dimension: "currency", ToBase: 1},
	{Symbol: "GBP", Dimension: "currency", ToBase: 1},
	{Symbol: "JPY", Dimension: "currency", ToBase: 1},
	{Symbol: "CNY", Dimension: "currency", ToBase: 1},
	{Symbol: "CHF", Dimension: "currency", ToBase: 1},
	{Symbol: "CAD", Dimension: "currency", ToBase: 1},
	{Symbol: "AUD", Dimension: "currency", ToBase: 1},

	{Symbol: "BTC", Dimension: "cryptocurrency", ToBase: 1},
	{Symbol: "ETH", Dimension: "cryptocurrency", ToBase: 1},
	{Symbol: "SOL", Dimension: "cryptocurrency", ToBase: 1},
	{Symbol: "USDC", Dimension: "cryptocurrency", ToBase: 1},
	{Symbol: "USDT", Dimension: "cryptocurrency", ToBase: 1},
}

var (
	bySymbol    map[string]Entry
	byDimension map[string]struct{}
)

func init() {
	bySymbol = make(map[string]Entry, len(entries))
	byDimension = make(map[string]struct{})
	for _, entry := range entries {
		bySymbol[entry.Symbol] = entry
		byDimension[entry.Dimension] = struct{}{}
	}
}

// Lookup returns the unit entry for one symbol.
func Lookup(symbol string) (Entry, bool) {
	entry, ok := bySymbol[symbol]
	return entry, ok
}

// KnownDimension reports whether the dimension exists in the built-in table.
func KnownDimension(dimension string) bool {
	_, ok := byDimension[dimension]
	return ok
}

// SymbolsForDimension returns the built-in symbols for one dimension.
func SymbolsForDimension(dimension string) []string {
	if dimension == "" {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.Dimension == dimension {
			out = append(out, entry.Symbol)
		}
	}
	sort.Strings(out)
	return out
}

// Normalize converts one quantity to its dimension's base unit.
func Normalize(value float64, symbol string) (float64, Entry, error) {
	entry, ok := Lookup(symbol)
	if !ok {
		return 0, Entry{}, fmt.Errorf("unknown unit %q", symbol)
	}
	return value*entry.ToBase + entry.Offset, entry, nil
}
