package govern_test

import (
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
)

// TestRequestCacheEvalSegmentKeepsErrorOnCacheHit pins that a segment whose
// condition fails at runtime reports that error on every lookup in the same
// request, not only the first. A memoized plain false would turn the second
// lookup into a silent non-match.
func TestRequestCacheEvalSegmentKeepsErrorOnCacheHit(t *testing.T) {
	prog, err := arbiter.Compile([]byte(`rule seg { when { user.balance > 10.50 USD } then Match {} }`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	segments := govern.NewSegmentSet()
	segments.Add(&govern.CompiledSegment{
		Name:    "bad_compare",
		Source:  `user.balance > 10.50 USD`,
		Ruleset: prog.Ruleset,
	})
	rc := govern.NewRequestCache(segments, map[string]any{
		"user": map[string]any{"balance": "enterprise"},
	})

	for i := 1; i <= 2; i++ {
		matched, detail, err := rc.EvalSegment("bad_compare")
		if err == nil {
			t.Fatalf("lookup %d: expected the runtime error, got nil (detail %q)", i, detail)
		}
		if matched {
			t.Fatalf("lookup %d: expected matched=false alongside the error", i)
		}
	}
}
