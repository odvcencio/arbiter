package govern_test

import (
	"testing"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/govern"
)

func TestCompiledSegmentEval(t *testing.T) {
	rs, err := arbiter.Compile([]byte(`rule seg { when { user.plan == "enterprise" } then Match {} }`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	seg := &govern.CompiledSegment{
		Name:    "enterprise",
		Source:  `user.plan == "enterprise"`,
		Ruleset: rs,
	}

	ok := seg.Eval(map[string]any{
		"user": map[string]any{
			"plan": "enterprise",
		},
	})
	if !ok {
		t.Fatal("expected segment to match")
	}
}
