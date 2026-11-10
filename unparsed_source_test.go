package arbiter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/arbiter/ir"
)

// Three expert rules, the middle one a sustained-usage rule with "for 1h"
// (goetrope policies/cache.arb, before it was changed to 1hr).
const cacheExpertSource = `
expert rule A priority 0 {
    when { any reading in facts.CacheReading { reading.volume_usage_pct > 90 } }
    then emit CacheAlert { severity: "critical", }
}

expert rule SustainedHighUsage priority 0 {
    when { any reading in facts.CacheReading { reading.volume_usage_pct > 85 } } for 1h
    then emit CacheAlert { severity: "warning", reason: "cache volume sustained above 85% for 1 hour", }
}

expert rule C priority 1 {
    when { any reading in facts.CacheReading { reading.volume_usage_pct > 50 } } within 30m
    then emit CacheAlert { severity: "info", }
}
`

func writeTemp(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.arb")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHourDurationKeepsEveryExpertRule(t *testing.T) {
	full, err := CompileFull([]byte(cacheExpertSource))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
	if got := len(full.Program.Expert); got != 3 {
		t.Fatalf("expert rules = %d, want 3", got)
	}
	fromFile, err := CompileFullFile(writeTemp(t, cacheExpertSource))
	if err != nil {
		t.Fatalf("CompileFullFile: %v", err)
	}
	if got := len(fromFile.Program.Expert); got != 3 {
		t.Fatalf("expert rules from file = %d, want 3", got)
	}
}

// Every case must fail with the same line and column on the in-memory path
// (Compile) and on the file path (CompileFile), and the file path must name
// the file. Cases include junk between declarations, which the old file path
// accepted.
func TestUnparsedSourceSamePositionOnBothPaths(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		line, col int
	}{
		{"when", "rule R priority 1 {\n    when { x > 1 @@ }\n    then ok {}\n}\n", 2, 18},
		{"then", "rule R priority 1 {\n    when { x > 1 }\n    then ok { a: 1 } @@\n}\n", 3, 22},
		{"between rules", "rule A { when { x > 1 } then ok {} }\n\n@@ junk\n\nrule B { when { x > 2 } then ok {} }\n", 3, 1},
		{"after last rule", "rule A { when { x > 1 } then ok {} }\n@@\n", 2, 1},
		{"before first rule", "@@\nrule A { when { x > 1 } then ok {} }\n", 1, 1},
		{"missing final brace", "rule A { when { x > 1 } then ok {} }\n\nrule B { when { x > 2 } then ok {\n", 3, 1},
		{"missing final brace expert", "expert rule B priority 0 {\n    when { x > 2 }\n    then emit O {\n", 1, 1},
		{"bad duration", "expert rule R priority 0 {\n    when { any r in facts.F { r.x > 1 } } for 1w\n    then emit O { a: 1, }\n}\n", 2, 47},
		{"junk on a late line", strings.Repeat("rule A { when { x > 1 } then ok {} }\n", 57) + "rule Z { when { x > 1 } then ok {} }\n@@\n", 59, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, memErr := Compile([]byte(tc.src))
			path := writeTemp(t, tc.src)
			_, fileErr := CompileFile(path)
			for label, err := range map[string]error{"Compile": memErr, "CompileFile": fileErr} {
				if err == nil {
					t.Fatalf("%s: expected an error, got nil", label)
				}
				var cov *ir.SourceCoverageError
				var diag *DiagnosticError
				if !errors.As(err, &cov) {
					// A parser error is acceptable only if it is positioned.
					if !errors.As(err, &diag) && !strings.Contains(err.Error(), ":") {
						t.Fatalf("%s: unpositioned error %v", label, err)
					}
					continue
				}
				if cov.Line != tc.line || cov.Col != tc.col {
					t.Errorf("%s: position %d:%d, want %d:%d (%v)", label, cov.Line, cov.Col, tc.line, tc.col, err)
				}
				if !errors.As(err, &diag) || diag.Line != tc.line || diag.Column != tc.col {
					t.Errorf("%s: want a DiagnosticError at %d:%d, got %v", label, tc.line, tc.col, err)
					continue
				}
				if label == "CompileFile" && diag.File != path {
					t.Errorf("CompileFile: diagnostic file = %q, want %q", diag.File, path)
				}
			}
		})
	}
}

// Failing the same way on both paths is the point; this test also fails if a
// case silently compiles on either path.
func TestUnparsedSourceNeverCompilesSilently(t *testing.T) {
	for _, src := range []string{
		"rule A { when { x > 1 } then ok {} }\n\n@@ junk\n\nrule B { when { x > 2 } then ok {} }\n",
		"rule A { when { x > 1 } then ok {} }\nrule B { when { x > 2 } then ok {\n",
	} {
		if _, err := Compile([]byte(src)); err == nil {
			t.Errorf("Compile accepted %q", src)
		}
		if _, err := CompileFile(writeTemp(t, src)); err == nil {
			t.Errorf("CompileFile accepted %q", src)
		}
	}
}

func TestPercentMessageKeptForBareRemainderToken(t *testing.T) {
	_, err := Compile([]byte("rule R { when { true } then Out { value: % } }\n"))
	if err == nil || !strings.Contains(err.Error(), "use pct") {
		t.Fatalf("err = %v, want the use-pct message", err)
	}
}
