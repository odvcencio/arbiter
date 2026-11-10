package main

import "testing"

// Unparsed source must be reported at its own line and column, not at 1:1.
func TestUntitledDiagnosticsLocateUnparsedSource(t *testing.T) {
	src := "rule R priority 1 {\n    when { x > 1 @@ }\n    then ok {}\n}\n"
	diags := compileUntitledDiagnostics(src)
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic")
	}
	start := diags[0]["range"].(map[string]any)["start"].(map[string]any)
	if start["line"] != 1 || start["character"] != 17 {
		t.Errorf("diagnostic at %v:%v, want 1:17 (zero-based; source line 2, column 18)", start["line"], start["character"])
	}
}
