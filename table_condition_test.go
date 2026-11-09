package arbiter

import (
	"reflect"
	"testing"
)

// A lookup in a rule's when block (the decision-table pattern) used to break
// the rule: the compiler recorded the condition's offset before appending the
// lookup's where code, so the rule ran the where code as its condition.
func TestLookupInConditionSelectsRow(t *testing.T) {
	prog, err := Compile([]byte(`
table bands {
    lo: number | hi: number | label: string | bonus: number
    0          | 10         | "low"         | 1
    10         | 20         | "mid"         | 2
    20         | 99         | "high"        | 0
}

rule Band {
    when {
        let row = lookup bands where lo <= hp and hi > hp
        row.bonus > 0
    }
    then Banded { label: row.label, bonus: row.bonus }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	for hp, want := range map[float64]any{5: "low", 15: "mid", 25: nil, 500: nil} {
		matched, err := Eval(prog, DataFromMap(map[string]any{"hp": hp}, prog))
		if err != nil {
			t.Fatalf("hp=%v: %v", hp, err)
		}
		if want == nil {
			if len(matched) != 0 {
				t.Errorf("hp=%v: matched %+v, want none", hp, matched)
			}
			continue
		}
		if len(matched) != 1 || matched[0].Params["label"] != want {
			t.Errorf("hp=%v: got %+v, want label %v", hp, matched, want)
		}
	}
}

// A let in a then block is prepended to the first parameter's bytecode. Every
// parameter must see it, not only the first.
func TestActionLetReachesEveryParameter(t *testing.T) {
	prog, err := Compile([]byte(`
table ladder {
    height: number | video: string | audio: string
    1080           | "6500k"       | "160k"
    720            | "3800k"       | "128k"
}

rule Transcode {
    when { true }
    then Profile {
        let row = lookup ladder where height <= target order by height desc
        let k = target * 2
        video: row.video,
        audio: row.audio,
        doubled: k,
    }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	matched, err := Eval(prog, DataFromMap(map[string]any{"target": 900.0}, prog))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"video": "3800k", "audio": "128k", "doubled": 1800.0}
	if len(matched) != 1 || !reflect.DeepEqual(matched[0].Params, want) {
		t.Fatalf("got %+v, want params %v", matched, want)
	}
}
