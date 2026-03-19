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

func writeCLIFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
