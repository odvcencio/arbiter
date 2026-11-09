package arena_test

import (
	"bytes"
	"flag"
	"os"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle/wire"
	"m31labs.dev/arbiter/examples/gameai/arena"
)

var update = flag.Bool("update", false, "rewrite arena.arbb from arena.arb")

// TestBundleIsCurrent fails when arena.arbb was not rebuilt after an edit to
// arena.arb, so the shipped bundle and the source cannot drift apart.
func TestBundleIsCurrent(t *testing.T) {
	prog, err := arbiter.Compile(arena.Source)
	if err != nil {
		t.Fatalf("compile arena.arb: %v", err)
	}
	want, err := wire.Marshal(prog.Ruleset)
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile("arena.arbb", want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(arena.Bundle, want) {
		t.Fatalf("arena.arbb is stale (%d bytes, want %d); run: go test ./examples/gameai/arena -run TestBundleIsCurrent -update", len(arena.Bundle), len(want))
	}
}
