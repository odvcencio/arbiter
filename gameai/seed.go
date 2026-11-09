package gameai

import "math/bits"

// Randomness in a deterministic simulation must come from state every peer
// shares. The helpers here derive it from integers with fixed arithmetic, so
// they give the same bits on every target. Prefer Seed over Rand: a seed
// derived from (match seed, tick, agent, decision) needs no state at all, so
// there is nothing to snapshot, and replaying any tick reproduces its draws.

const golden = 0x9e3779b97f4a7c15

// mix64 is the splitmix64 finalizer.
func mix64(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// Mix combines two values into one well-mixed 64-bit value.
func Mix(a, b uint64) uint64 { return mix64(a + golden*(mix64(b)|1)) }

// Seed derives a seed from a match seed and identifiers such as the tick, the
// agent and the decision: Seed(match, tick, agent, Key("tactic")).
func Seed(match uint64, parts ...uint64) uint64 {
	h := mix64(match ^ golden)
	for _, p := range parts {
		h = Mix(h, p)
	}
	return h
}

// Key hashes a name (FNV-1a, 64-bit) for use as a Seed part.
func Key(name string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(name); i++ {
		h ^= uint64(name[i])
		h *= 1099511628211
	}
	return h
}

// Unit maps a seed to a float64 in [0, 1) using its top 53 bits. The result is
// exact, so it is the same on every target. Pass it to rules as an input (for
// example a `roll` field) to make a rule fire with some probability.
func Unit(seed uint64) float64 { return float64(seed>>11) * 0x1p-53 }

// Rand is a splitmix64 generator for games that want a stream of draws. Its
// whole state is State: copy the struct to snapshot it.
type Rand struct{ State uint64 }

// Uint64 returns the next 64 random bits.
func (r *Rand) Uint64() uint64 {
	r.State += golden
	return mix64(r.State)
}

// Float64 returns a number in [0, 1).
func (r *Rand) Float64() float64 { return Unit(r.Uint64()) }

// Intn returns a number in [0, n) by multiply-shift; it returns 0 for n <= 0.
func (r *Rand) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	hi, _ := bits.Mul64(r.Uint64(), uint64(n))
	return int(hi)
}
