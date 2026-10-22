package units

import (
	"math"
	"testing"
)

// 56 F normalizes to 0x4071e7bbbbbbbbbc on amd64 and wasm. Before the fix,
// arm64 fused the multiply and add and returned ...bbbb.
func TestNormalizeFahrenheitIsBitStable(t *testing.T) {
	got, _, err := Normalize(56, "F")
	if err != nil {
		t.Fatal(err)
	}
	if bits := math.Float64bits(got); bits != 0x4071e7bbbbbbbbbc {
		t.Fatalf("56 F = %#x, want 0x4071e7bbbbbbbbbc", bits)
	}
}
