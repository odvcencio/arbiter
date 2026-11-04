package main

import (
	"bytes"
	"strings"
	"testing"

	arbiter "m31labs.dev/arbiter"
)

func TestReplEvaluatesContexts(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`rule Big { when { score > 10 } then Flag { level: "high" } }`))
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"score": 20}` + "\n" + `{"score": 5}` + "\nexit\n")
	var out bytes.Buffer
	if err := repl(prog, in, &out); err != nil {
		t.Fatalf("repl: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "Big") {
		t.Fatalf("first context (score 20) should match rule Big: %q", s)
	}
	if !strings.Contains(s, "no rules matched") {
		t.Fatalf("second context (score 5) should match nothing: %q", s)
	}
}

func TestReplAppliesGovernance(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`
segment eligible { amount > 10 }
rule Eligible { when segment eligible then Approve {} }
rule Disabled { kill_switch on when { true } then Deny {} }
`))
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader("{\"amount\":5}\n{\"amount\":20}\nexit\n")
	var out bytes.Buffer
	if err := repl(prog, in, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "(no rules matched)") || strings.Count(got, "Eligible -> Approve") != 1 || strings.Contains(got, "Disabled") {
		t.Fatalf("unexpected REPL decisions: %q", got)
	}
}

func TestReplReportsBadJSON(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`rule R { when { score > 1 } then A {} }`))
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader("not json\nexit\n")
	var out bytes.Buffer
	if err := repl(prog, in, &out); err != nil {
		t.Fatalf("repl should not error on bad input line: %v", err)
	}
	if !strings.Contains(out.String(), "error") {
		t.Fatalf("bad JSON should report an error line: %q", out.String())
	}
}
