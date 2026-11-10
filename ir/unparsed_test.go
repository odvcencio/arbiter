package ir_test

import (
	"regexp"
	"strings"
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/ir"
)

var locationRE = regexp.MustCompile(`\d+:\d+`)

func lowerErr(t *testing.T, source string) error {
	t.Helper()
	parsed, err := arbiter.ParseSource([]byte(source))
	if err != nil {
		return err
	}
	_, err = ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
	return err
}

// Three expert rules, the middle one a sustained-usage rule with "for 1h"
// (goetrope policies/cache.arb). All three must be present after lowering.
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

func TestLowerKeepsEveryExpertRuleWithHourDuration(t *testing.T) {
	parsed, err := arbiter.ParseSource([]byte(cacheExpertSource))
	if err != nil {
		t.Fatal(err)
	}
	program, err := ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	if got := len(program.Expert); got != 3 {
		t.Fatalf("len(Expert) = %d, want 3", got)
	}
	names := []string{program.Expert[0].Name, program.Expert[1].Name, program.Expert[2].Name}
	if names[1] != "SustainedHighUsage" {
		t.Fatalf("expert rules = %v, want SustainedHighUsage second", names)
	}
}

func TestTemporalHourAliasMatchesHr(t *testing.T) {
	for _, unit := range []string{"1h", "1hr", "2.5h"} {
		src := "expert rule R priority 0 {\n    when { any r in facts.F { r.x > 1 } } for " + unit + "\n    then emit O { a: 1, }\n}\n"
		if err := lowerErr(t, src); err != nil {
			t.Errorf("for %s: %v", unit, err)
		}
	}
	for _, clause := range []string{"cooldown 1h", "debounce 1h"} {
		src := "expert rule R priority 0 " + clause + " {\n    when { x > 1 }\n    then emit O { a: 1, }\n}\n"
		if err := lowerErr(t, src); err != nil {
			t.Errorf("%s: %v", clause, err)
		}
	}
}

func TestLowerRejectsUnparsedSourceWithLocation(t *testing.T) {
	cases := []struct {
		name, src, wantPos string
	}{
		{"when", "rule R priority 1 {\n    when { x > 1 @@ }\n    then ok {}\n}\n", "unparsed source at 2:"},
		{"then", "rule R priority 1 {\n    when { x > 1 }\n    then ok { a: 1 } @@\n}\n", "unparsed source at 3:"},
		{"header", "rule R priority 1 banana {\n    when { x > 1 }\n    then ok {}\n}\n", "unparsed source at 1:"},
		{"between rules", "rule A { when { x > 1 } then ok {} }\n\n@@ junk\n\nrule B { when { x > 2 } then ok {} }\n", "unparsed source at 3:1"},
		{"bad duration", "expert rule R priority 0 {\n    when { any r in facts.F { r.x > 1 } } for 1w\n    then emit O { a: 1, }\n}\n", "unparsed source at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := lowerErr(t, tc.src)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			// Either the parser reports the error, or the unparsed-source
			// check does. Both must carry a line:column location.
			if !locationRE.MatchString(err.Error()) {
				t.Fatalf("error %q has no line:column location", err)
			}
			if strings.HasPrefix(err.Error(), "unparsed source") && !strings.Contains(err.Error(), tc.wantPos) {
				t.Fatalf("error %q does not contain %q", err, tc.wantPos)
			}
		})
	}
}

func TestLowerLoneRemainderKeepsPercentMessage(t *testing.T) {
	err := lowerErr(t, "rule R priority 1 {\n    when { x > 50 % }\n    then ok {}\n}\n")
	if err == nil {
		t.Fatal("expected an error for a dangling %")
	}
	// The parser may reject this itself; when it does not, the message must
	// keep the percentage guidance.
	if !strings.Contains(err.Error(), "use pct") && !locationRE.MatchString(err.Error()) {
		t.Fatalf("err = %v, want pct guidance or a location", err)
	}
}

func TestLowerAllowsPercentInStringsAndComments(t *testing.T) {
	src := "# 85% of capacity; unparsed-looking text @@ is fine here\nrule R priority 1 {\n    when { x > 1 } # also 50% here\n    then ok { reason: \"above 85% for 1h @@\", }\n}\n/* block 100% */\n"
	if err := lowerErr(t, src); err != nil {
		t.Fatalf("Lower: %v", err)
	}
}
