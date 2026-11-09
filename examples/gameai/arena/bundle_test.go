package arena_test

import (
	"bytes"
	"flag"
	"os"
	"testing"

	"m31labs.dev/arbiter/examples/gameai/arena"
	"m31labs.dev/arbiter/gameai/build"
)

var update = flag.Bool("update", false, "rewrite arena.arbb from arena.arb")

// TestBundleIsCurrent fails when arena.arbb was not rebuilt after an edit to
// arena.arb, so the shipped bundle and the source cannot drift apart. It
// builds the bundle the way `arbiter bundle --game --keep-names` does.
func TestBundleIsCurrent(t *testing.T) {
	res, err := build.Compile(arena.Source, build.Options{KeepNames: true})
	if err != nil {
		t.Fatalf("build arena.arb: %v", err)
	}
	if *update {
		if err := os.WriteFile("arena.arbb", res.Bundle, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(arena.Bundle, res.Bundle) {
		t.Fatalf("arena.arbb is stale (%d bytes, want %d); run: arbiter bundle examples/gameai/arena/arena.arb --game --keep-names", len(arena.Bundle), len(res.Bundle))
	}
}

// TestHashedBundlePlaysTheSameMatch builds the bundle with hashed rule names,
// as a shipped game might, and checks that it makes the same decisions.
func TestHashedBundlePlaysTheSameMatch(t *testing.T) {
	res, err := build.Compile(arena.Source, build.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hashed, err := arena.NewMatch(res.Program, 7)
	if err != nil {
		t.Fatal(err)
	}
	plain := newMatch(t, 7)
	for i := 0; i < 600; i++ {
		if err := hashed.Step(); err != nil {
			t.Fatal(err)
		}
		if err := plain.Step(); err != nil {
			t.Fatal(err)
		}
	}
	if hashed.State != plain.State {
		t.Fatalf("hashed bundle diverged: digest %#x vs %#x", hashed.Digest, plain.Digest)
	}
	if res.Program.Rules()[0] == "Chase" {
		t.Fatal("rule names were not hashed")
	}
}
