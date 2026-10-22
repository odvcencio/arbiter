package expert_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"m31labs.dev/arbiter/expert"
)

// Go randomizes map iteration per range loop. These rules read fact lists in
// ways that expose list order: an `any` binding, a per_fact rule and a float sum.
const determinismSource = `
expert rule Pick priority 10 {
	when { any f in facts.Item { f.n >= 10 } }
	then emit Picked { key: f.key }
}
expert rule Each priority 5 per_fact {
	when { any f in facts.Item { f.n >= 40 } }
	then emit Each { key: f.key }
}
expert rule Total priority 1 {
	when { count(f.n for f in facts.Item) > 0 }
	then emit Total { sum: sum(f.v for f in facts.Item) }
}
`

func TestExpertSessionIsDeterministicAcrossRuns(t *testing.T) {
	p, err := expert.Compile([]byte(determinismSource))
	if err != nil {
		t.Fatal(err)
	}
	var want string
	for run := 0; run < 25; run++ {
		var facts []expert.Fact
		for i := 0; i < 60; i++ {
			facts = append(facts, expert.Fact{Type: "Item", Key: fmt.Sprintf("k%02d", i), Fields: map[string]any{
				"n": float64(i), "v": 1e16/float64(i+1) + 0.1*float64(i%7),
			}})
		}
		res, err := expert.NewSession(p, nil, facts, expert.Options{}).Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		blob, _ := json.Marshal(res.Outcomes)
		if run == 0 {
			want = string(blob)
			continue
		}
		if string(blob) != want {
			t.Fatalf("run %d differs:\n got: %s\nwant: %s", run, blob, want)
		}
	}
}
