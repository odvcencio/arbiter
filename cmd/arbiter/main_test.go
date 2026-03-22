package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/arbiter"
)

func TestFormatCLIErrorPreservesDiagnostics(t *testing.T) {
	err := &arbiter.DiagnosticError{
		File:    "/tmp/rules.arb",
		Line:    4,
		Column:  9,
		Message: "parse error near \"then\"",
	}
	if got := formatCLIError(err); got != `/tmp/rules.arb:4:9: parse error near "then"` {
		t.Fatalf("unexpected diagnostic formatting: %q", got)
	}
}

func TestFormatCLIErrorUnwrapsWrappedDiagnostics(t *testing.T) {
	err := &arbiter.DiagnosticError{
		File:    "/tmp/rules.arb",
		Line:    4,
		Column:  9,
		Message: "parse error near \"then\"",
	}
	wrapped := fmt.Errorf("check current file: %w", err)
	if got := formatCLIError(wrapped); got != `/tmp/rules.arb:4:9: parse error near "then"` {
		t.Fatalf("unexpected wrapped diagnostic formatting: %q", got)
	}
}

func TestFormatCLIErrorPreservesPathPositionStrings(t *testing.T) {
	err := errors.New(`/tmp/rules.arb:3:5: parse error near "}"`)
	if got := formatCLIError(err); got != err.Error() {
		t.Fatalf("expected raw diagnostic string, got %q", got)
	}
}

func TestFormatCLIErrorPrefixesGenericErrors(t *testing.T) {
	err := errors.New("boom")
	if got := formatCLIError(err); got != "error: boom" {
		t.Fatalf("unexpected generic formatting: %q", got)
	}
}

func TestCheckRejectsCompileErrorsInIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	mainPath := writeCLIFile(t, dir, "main.arb", `include "bad.arb"`)
	badPath := writeCLIFile(t, dir, "bad.arb", `
rule BadRollout {
	when { true }
	then Approved {}
	rollout 101
}
`)

	err := check(mainPath)
	if err == nil {
		t.Fatal("expected check to fail")
	}
	if !strings.Contains(err.Error(), badPath+":2:1:") {
		t.Fatalf("expected included file diagnostic, got %v", err)
	}
}

func TestExploreCmdPrintsBundleSummary(t *testing.T) {
	dir := t.TempDir()
	path := writeCLIFile(t, dir, "bundle.arb", `
const SAFE_TEMP = 28 C

fact SensorReading {
	temperature: number<temperature>
}

expert rule HeatStress {
	when { input.hot == true } for 10m
	then emit HeatWarning {
		zone: "zone-a",
	}
}
`)

	out := captureStdout(t, func() {
		if err := exploreCmd(path); err != nil {
			t.Fatalf("exploreCmd: %v", err)
		}
	})
	if !strings.Contains(out, `"fact_schemas"`) || !strings.Contains(out, `"expert_rules"`) {
		t.Fatalf("expected explore output to include schemas and expert rules, got %s", out)
	}
	if !strings.Contains(out, `"SAFE_TEMP"`) {
		t.Fatalf("expected explore output to include constants, got %s", out)
	}
}

func TestTestCmdRunsSpecs(t *testing.T) {
	dir := t.TempDir()
	bundlePath := writeCLIFile(t, dir, "bundle.arb", `
rule FreeShipping {
	when { user.cart_total >= 35 }
	then ApplyShipping { cost: 0 }
}
`)
	testPath := writeCLIFile(t, dir, "bundle.test.arb", `
test "shipping applies" {
	given {
		user.cart_total: 50
	}
	expect rule FreeShipping matched
	expect action ApplyShipping { cost: 0 }
}
`)

	_ = bundlePath
	out := captureStdout(t, func() {
		if err := testCmd(testPath, true); err != nil {
			t.Fatalf("testCmd: %v", err)
		}
	})
	if !strings.Contains(out, "1 passed, 0 failed") {
		t.Fatalf("expected passing test summary, got %s", out)
	}
}

func writeCLIFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
