package expert

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// "h" is an alias of "hr" in temporal durations. These tests go through the
// full compile (Compile and CompileFile), not just lowering, and check the
// resulting time.Duration.
func TestTemporalHourAliasCompilesToOneHour(t *testing.T) {
	src := `expert rule R priority 0 cooldown 1h {
    when { any r in facts.F { r.x > 1 } } for 1h
    then emit O { a: 1, }
}

expert rule D priority 0 debounce 2h {
    when { x > 1 }
    then emit O { a: 1, }
}

expert rule W priority 0 {
    when { any r in facts.F { r.x > 1 } } within 1h
    then emit O { a: 1, }
}

expert rule Hr priority 0 {
    when { any r in facts.F { r.x > 1 } } for 1hr
    then emit O { a: 1, }
}
`
	path := filepath.Join(t.TempDir(), "hours.arb")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	inMemory, err := Compile([]byte(src))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	fromFile, err := CompileFile(path)
	if err != nil {
		t.Fatalf("CompileFile: %v", err)
	}
	for name, program := range map[string]*Program{"Compile": inMemory, "CompileFile": fromFile} {
		rules := map[string]Rule{}
		for _, r := range program.Rules() {
			rules[r.Name] = r
		}
		if len(rules) != 4 {
			t.Fatalf("%s: got %d rules, want 4", name, len(rules))
		}
		r := rules["R"]
		if r.ForDuration != time.Hour || r.Cooldown != time.Hour {
			t.Errorf("%s: R for/cooldown = %v/%v, want 1h/1h", name, r.ForDuration, r.Cooldown)
		}
		if got := rules["D"].Debounce; got != 2*time.Hour {
			t.Errorf("%s: D debounce = %v, want 2h", name, got)
		}
		if got := rules["W"].WithinDuration; got != time.Hour {
			t.Errorf("%s: W within = %v, want 1h", name, got)
		}
		if got := rules["Hr"].ForDuration; got != time.Hour {
			t.Errorf("%s: Hr for = %v, want 1h", name, got)
		}
	}
}
