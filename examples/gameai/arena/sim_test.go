package arena_test

import (
	"testing"

	"m31labs.dev/arbiter/examples/gameai/arena"
	"m31labs.dev/arbiter/gameai"
)

func TestMatchSmoke(t *testing.T) {
	prog, err := gameai.Load(arena.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	m, err := arena.NewMatch(prog, 7)
	if err != nil {
		t.Fatal(err)
	}
	m.Watch = 3
	counts := map[uint8]int{}
	for i := 0; i < 900; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
		for _, b := range m.Bots {
			counts[b.Intent]++
		}
	}
	t.Logf("score %v digest %016x intents %v", m.Score, m.Digest, counts)
	t.Logf("trace:\n%s", m.Trace.String())
}
