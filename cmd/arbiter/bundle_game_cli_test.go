package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/arbiter/gameai"
	"m31labs.dev/arbiter/gameai/build"
)

// arbiter bundle --game writes a gameai bundle: deterministic, loadable, not
// blocked by the edge-export risk policy, and rejected when the rules read
// the wall clock.
func TestBundleGameCLI(t *testing.T) {
	dir := t.TempDir()
	src := []byte(`tag "act"
rule Risky tag "act" { when { risk.score > 10 and risk.score > 20 and risk.score > 30 } then A { kind: "x" } }
rule Calm tag "act" { when { true } then A { kind: "calm" } }
`)
	arbPath := filepath.Join(dir, "ai.arb")
	if err := os.WriteFile(arbPath, src, 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "ai.arbb")
	if err := runBundle([]string{arbPath, "--game", "--keep-names", "-o", outPath}); err != nil {
		t.Fatalf("bundle --game: %v", err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := build.CompileFile(arbPath, build.Options{KeepNames: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Bundle) {
		t.Fatal("CLI output differs from build.CompileFile")
	}
	p, err := gameai.Load(got)
	if err != nil {
		t.Fatal(err)
	}
	if p.Rules()[0] != "Risky" {
		t.Fatalf("rules %v", p.Rules())
	}

	clock := filepath.Join(dir, "clock.arb")
	if err := os.WriteFile(clock, []byte(`rule R { active_from 2026-01-01T00:00:00Z when { true } then A {} }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runBundle([]string{clock, "--game", "-o", filepath.Join(dir, "clock.arbb")}); err == nil {
		t.Fatal("bundle --game accepted an active window")
	}
}
