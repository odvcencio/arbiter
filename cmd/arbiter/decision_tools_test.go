package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/arbiter/audit"
)

func TestDiffCmdJSONOutput(t *testing.T) {
	dir := t.TempDir()
	basePath := writeCLIFile(t, dir, "base.arb", `
rule ApproveSmall {
	when { amount < 100 }
	then Approve { tier: "small" }
}

rule ReviewLarge {
	when { amount >= 100 }
	then Review {}
}
`)
	candidatePath := writeCLIFile(t, dir, "candidate.arb", `
rule ApproveSmall {
	when { amount < 100 }
	then Approve { tier: "small" }
}

rule EscalateLarge {
	when { amount >= 100 }
	then Escalate { lane: "risk" }
}
`)

	out := captureStdout(t, func() {
		err := diffCmd(basePath, candidatePath, `[{"request_id":"req-1","amount":25},{"request_id":"req-2","amount":150}]`, "", "request_id", true)
		if err != nil {
			t.Fatalf("diffCmd: %v", err)
		}
	})

	var report diffReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("unmarshal diff report: %v\noutput: %s", err, out)
	}
	if report.Compared != 2 || report.Changed != 1 || report.Unchanged != 1 {
		t.Fatalf("unexpected diff summary: %+v", report)
	}
	if len(report.Differences) != 1 || report.Differences[0].Key != "req-2" {
		t.Fatalf("unexpected differences: %+v", report.Differences)
	}
	diff := report.Differences[0]
	if len(diff.Removed) != 1 || diff.Removed[0].Name != "ReviewLarge" {
		t.Fatalf("expected removed ReviewLarge, got %+v", diff.Removed)
	}
	if len(diff.Added) != 1 || diff.Added[0].Name != "EscalateLarge" {
		t.Fatalf("expected added EscalateLarge, got %+v", diff.Added)
	}
}

func TestReplayCmdJSONOutput(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeCLIFile(t, dir, "candidate.arb", `
rule EscalateLarge {
	when { amount >= 100 }
	then Escalate { lane: "risk" }
}
`)
	auditPath := filepath.Join(dir, "decisions.jsonl")
	record := audit.DecisionEvent{
		Kind:      "rules",
		RequestID: "req-2",
		Context: map[string]any{
			"amount": 150,
		},
		Rules: []audit.RuleMatch{{
			Name:   "ReviewLarge",
			Action: "Review",
		}},
	}
	writeJSONL(t, auditPath, record)

	out := captureStdout(t, func() {
		err := replayCmd(rulesPath, auditPath, "", 0, true)
		if err != nil {
			t.Fatalf("replayCmd: %v", err)
		}
	})

	var report replayReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("unmarshal replay report: %v\noutput: %s", err, out)
	}
	if report.Replayed != 1 || report.Changed != 1 || report.Unchanged != 0 {
		t.Fatalf("unexpected replay summary: %+v", report)
	}
	if len(report.Differences) != 1 || report.Differences[0].Key != "req-2" {
		t.Fatalf("unexpected replay differences: %+v", report.Differences)
	}
	diff := report.Differences[0]
	if len(diff.Removed) != 1 || diff.Removed[0].Name != "ReviewLarge" {
		t.Fatalf("expected removed ReviewLarge, got %+v", diff.Removed)
	}
	if len(diff.Added) != 1 || diff.Added[0].Name != "EscalateLarge" {
		t.Fatalf("expected added EscalateLarge, got %+v", diff.Added)
	}
}

func TestParseNamedContextsUsesKeyPath(t *testing.T) {
	contexts, err := parseNamedContexts([]byte(`[{"meta":{"request_id":"req-7"},"amount":42}]`), "meta.request_id")
	if err != nil {
		t.Fatalf("parseNamedContexts: %v", err)
	}
	if len(contexts) != 1 {
		t.Fatalf("expected one context, got %d", len(contexts))
	}
	if contexts[0].Key != "req-7" {
		t.Fatalf("expected key req-7, got %q", contexts[0].Key)
	}
}

func TestSameRuleMatchTreatsNilAndEmptyParamsAsEqual(t *testing.T) {
	a := audit.RuleMatch{Name: "ReviewLarge", Action: "Review", Params: nil}
	b := audit.RuleMatch{Name: "ReviewLarge", Action: "Review", Params: map[string]any{}}
	if !sameRuleMatch(a, b) {
		t.Fatal("expected nil and empty params to compare equal")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	os.Stdout = w
	defer func() {
		os.Stdout = orig
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(out)
}

func writeJSONL(t *testing.T, path string, records ...audit.DecisionEvent) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, record := range records {
		if err := enc.Encode(record); err != nil {
			t.Fatalf("encode %s: %v", path, err)
		}
	}
}
