package arena_test

import (
	"testing"

	"m31labs.dev/arbiter/examples/gameai/arena"
	"m31labs.dev/arbiter/gameai"
)

func newMatch(t testing.TB, seed uint64) *arena.Match {
	t.Helper()
	prog, err := gameai.Load(arena.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	m, err := arena.NewMatch(prog, seed)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTickDoesNotAllocate checks the per-tick budget: 22 bots making three
// decisions each (66 evaluations, including table lookups, let bindings,
// memory reads and a weighted draw) allocate nothing once warm, with and
// without a trace on one watched bot.
func TestTickDoesNotAllocate(t *testing.T) {
	for _, watch := range []int{-1, 5} {
		m := newMatch(t, 11)
		m.Watch = watch
		for i := 0; i < 300; i++ { // warm up: grow every buffer
			if err := m.Step(); err != nil {
				t.Fatal(err)
			}
		}
		allocs := testing.AllocsPerRun(200, func() {
			if err := m.Step(); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 0 {
			t.Errorf("watch=%d: %.2f allocations per tick, want 0", watch, allocs)
		}
	}
}

// BenchmarkTick measures one fixed tick of the toy game: 22 bots x 3
// decisions, plus the toy simulation itself.
func BenchmarkTick(b *testing.B) {
	m := newMatch(b, 11)
	for i := 0; i < 300; i++ {
		if err := m.Step(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := m.Step(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/(arena.Bots*3), "ns/decision")
}

// BenchmarkTickTraced is BenchmarkTick with one bot's intent traced.
func BenchmarkTickTraced(b *testing.B) {
	m := newMatch(b, 11)
	m.Watch = 5
	for i := 0; i < 300; i++ {
		if err := m.Step(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := m.Step(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecisions measures the three decisions alone for 22 prepared
// views, without the toy simulation: one op is one tick's 66 decisions.
func BenchmarkDecisions(b *testing.B) {
	m := newMatch(b, 11)
	for i := 0; i < 300; i++ {
		if err := m.Step(); err != nil {
			b.Fatal(err)
		}
	}
	views := m.Views()
	brain := m.Brain()
	var ds []gameai.Decision[arena.View]
	for _, spec := range arena.Decisions {
		d, err := brain.Decision(spec.Tag)
		if err != nil {
			b.Fatal(err)
		}
		ds = append(ds, d)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range views {
			for k, d := range ds {
				if _, err := d.Decide(&views[j], uint64(i*97+j*7+k)); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(views)*len(ds)), "ns/decision")
}
