package arbiter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileRejectsInlineInclude(t *testing.T) {
	_, err := Compile([]byte(`include "constants.arb"`))
	if err == nil {
		t.Fatal("expected raw Compile to reject include declarations")
	}
	if !strings.Contains(err.Error(), "CompileFile") {
		t.Fatalf("expected include guidance in error, got %v", err)
	}
}

func TestCompileFullFileResolvesIncludesAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "constants.arb", `const LIMIT = 600`)
	writeTestFile(t, dir, "segments.arb", `
segment enterprise {
	user.plan == "enterprise"
}
`)
	writeTestFile(t, dir, "phase1.arb", `
rule BaseDecision {
	when { user.score >= LIMIT }
	then Seed { seeded: true }
}
`)
	writeTestFile(t, dir, "phase2.arb", `
rule EnterpriseDecision {
	requires BaseDecision
	when segment enterprise { true }
	then Approved { tier: "gold" }
}
`)
	main := writeTestFile(t, dir, "main.arb", `
include "constants.arb"
include "segments.arb"
include "phase1.arb"
include "phase2.arb"
`)

	result, err := CompileFullFile(main)
	if err != nil {
		t.Fatalf("CompileFullFile: %v", err)
	}
	if len(result.Ruleset.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(result.Ruleset.Rules))
	}
	if _, ok := result.Segments.Get("enterprise"); !ok {
		t.Fatal("expected enterprise segment to be available across files")
	}

	ctx := map[string]any{
		"user": map[string]any{
			"plan":  "enterprise",
			"score": 710.0,
		},
	}
	dc := DataFromMap(ctx, result.Ruleset)
	matched, _, err := EvalGoverned(result.Ruleset, dc, result.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	if len(matched) != 2 {
		t.Fatalf("expected 2 matches, got %+v", matched)
	}
	if matched[0].Name != "BaseDecision" || matched[1].Name != "EnterpriseDecision" {
		t.Fatalf("unexpected cross-file prerequisite order: %+v", matched)
	}
}

func TestCompileFullFileRejectsIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	main := writeTestFile(t, dir, "main.arb", `include "loop.arb"`)
	writeTestFile(t, dir, "loop.arb", `include "main.arb"`)

	_, err := CompileFullFile(main)
	if err == nil {
		t.Fatal("expected include cycle error")
	}
	if !strings.Contains(err.Error(), "include cycle") {
		t.Fatalf("expected include cycle in error, got %v", err)
	}
}

func TestCompileFileMapsSemanticErrorsToIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	bad := writeTestFile(t, dir, "bad.arb", `
rule BadRollout {
	when { true }
	then Approved {}
	rollout 101
}
`)
	main := writeTestFile(t, dir, "main.arb", `include "bad.arb"`)

	_, err := CompileFile(main)
	if err == nil {
		t.Fatal("expected compile error")
	}
	if got := err.Error(); !strings.Contains(got, bad+":2:1:") {
		t.Fatalf("expected included file diagnostic, got %v", err)
	}
	if !strings.Contains(err.Error(), "rule BadRollout: rollout must be between 0 and 100") {
		t.Fatalf("unexpected compile error: %v", err)
	}
}

func TestCompileFileMapsParseErrorsToIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	bad := writeTestFile(t, dir, "bad.arb", `
rule Broken {
	when { true
	then Approved {}
}
`)
	main := writeTestFile(t, dir, "main.arb", `include "bad.arb"`)

	_, err := CompileFile(main)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), bad+":") {
		t.Fatalf("expected included file path in parse error, got %v", err)
	}
	if !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("expected parse error detail, got %v", err)
	}
}

func TestTranspileFileResolvesIncludes(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "constants.arb", `const LIMIT = 100`)
	writeTestFile(t, dir, "rules.arb", `
rule Approve {
	when { cart.total >= LIMIT }
	then Approved { tier: "silver" }
}
`)
	main := writeTestFile(t, dir, "main.arb", `
include "constants.arb"
include "rules.arb"
`)

	out, err := TranspileFile(main)
	if err != nil {
		t.Fatalf("TranspileFile: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result["consts"] == nil {
		t.Fatalf("expected transpiled consts, got %s", out)
	}
	rules, ok := result["rules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("expected 1 transpiled rule, got %s", out)
	}
}

func TestLoadFileUnitTracksOriginalFileLinesAcrossIncludes(t *testing.T) {
	dir := t.TempDir()
	first := writeTestFile(t, dir, "first.arb", `rule First {
	when { true }
	then One {}
}`)
	second := writeTestFile(t, dir, "second.arb", `const LIMIT = 10`)
	main := writeTestFile(t, dir, "main.arb", `
include "first.arb"
include "second.arb"
`)

	unit, err := LoadFileUnit(main)
	if err != nil {
		t.Fatalf("LoadFileUnit: %v", err)
	}
	if len(unit.Origins) != 2 {
		t.Fatalf("expected 2 origins, got %+v", unit.Origins)
	}

	origin, ok := unit.OriginForLine(1)
	if !ok {
		t.Fatal("expected origin for generated line 1")
	}
	if origin.File != first || origin.SourceLine != 1 || origin.Name != "First" {
		t.Fatalf("unexpected origin for line 1: %+v", origin)
	}

	origin, ok = unit.OriginForLine(4)
	if !ok {
		t.Fatal("expected origin for generated line 4")
	}
	if origin.File != first || origin.SourceLine != 1 || origin.Name != "First" {
		t.Fatalf("unexpected origin for line 4: %+v", origin)
	}

	origin, ok = unit.OriginForLine(5)
	if !ok {
		t.Fatal("expected origin for generated line 5")
	}
	if origin.File != second || origin.SourceLine != 1 || origin.Name != "LIMIT" {
		t.Fatalf("unexpected origin for line 5: %+v", origin)
	}
}

func writeTestFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
