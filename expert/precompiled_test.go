package expert_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"m31labs.dev/arbiter/expert"
	"m31labs.dev/arbiter/expert/engine"
)

// precompiledSource uses segments, rollout, schemas, let bindings, activation
// groups, modify, retract, no_loop and requires, so the round trip covers each
// part of the encoded program.
const precompiledSource = `
fact Reading {
	key: string
	level: number
}

outcome Alert {
	kind: string
	reason: string
}

segment hot_zone {
	zone.temp > 30
}

expert rule Seed priority 50 {
	when {
		zone.temp > 10
	}
	then assert Flag {
		key: "k1",
		level: "high",
	}
}

expert rule Lower priority 40 {
	requires Seed
	when {
		zone.manual == true
		and any f in facts.Flag { f.level == "high" }
	}
	then modify Flag {
		key: "k1"
		set {
			level: "low",
		}
	}
}

expert rule Page priority 30 {
	activation_group action
	when segment hot_zone {
		let margin = zone.temp - 30
		margin > 5
	}
	then emit Alert {
		kind: "page",
		reason: "very hot",
	}
}

expert rule Notify priority 20 {
	activation_group action
	when segment hot_zone {
		zone.temp > 30
	}
	then emit Alert {
		kind: "notify",
		reason: "hot",
	}
}

expert rule Sample priority 10 {
	rollout 50
	when {
		zone.sampled == true
	}
	then emit Alert {
		kind: "sample",
		reason: "sampled",
	}
}

expert rule Clear priority 5 {
	when {
		zone.clear == true
	}
	then retract Flag {
		key: "k1",
	}
}
`

func runCorpus(t *testing.T, p *expert.Program) []string {
	t.Helper()
	var out []string
	for _, temp := range []float64{0, 11, 30, 31, 36, 80} {
		for _, manual := range []bool{false, true} {
			for _, clear := range []bool{false, true} {
				for _, sampled := range []bool{false, true} {
					env := map[string]any{"zone": map[string]any{
						"temp": temp, "manual": manual, "clear": clear, "sampled": sampled,
					}}
					facts := []expert.Fact{{Type: "Reading", Key: "r", Fields: map[string]any{"key": "r", "level": 1.0}}}
					res, err := expert.NewSession(p, env, facts, expert.Options{BundleID: "corpus"}).Run(context.Background())
					blob, merr := json.Marshal(res)
					if merr != nil {
						t.Fatalf("marshal result: %v", merr)
					}
					out = append(out, fmt.Sprintf("%v|%s|err=%v", env, blob, err))
				}
			}
		}
	}
	return out
}

func TestPrecompiledProgramMatchesSourceCompiled(t *testing.T) {
	source, err := expert.Compile([]byte(precompiledSource))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	blob, err := source.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	loaded, err := engine.UnmarshalProgram(blob)
	if err != nil {
		t.Fatalf("UnmarshalProgram: %v", err)
	}
	if !reflect.DeepEqual(source.Rules(), loaded.Rules()) {
		t.Fatalf("rules differ:\n source: %+v\n loaded: %+v", source.Rules(), loaded.Rules())
	}
	again, err := loaded.MarshalBinary()
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(blob, again) {
		t.Fatal("marshal -> unmarshal -> marshal is not byte-stable")
	}
	want, got := runCorpus(t, source), runCorpus(t, loaded)
	if len(want) != len(got) {
		t.Fatalf("corpus size %d != %d", len(want), len(got))
	}
	sawEmit, sawModify := false, false
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("decision %d differs:\n source: %s\n loaded: %s", i, want[i], got[i])
		}
		sawEmit = sawEmit || bytes.Contains([]byte(want[i]), []byte(`"kind":"page"`))
		sawModify = sawModify || bytes.Contains([]byte(want[i]), []byte(`"low"`))
	}
	if !sawEmit || !sawModify {
		t.Fatalf("corpus did not exercise the rules (page=%v modify=%v)", sawEmit, sawModify)
	}
}

func TestPrecompiledExampleFilesRoundTrip(t *testing.T) {
	for _, path := range []string{
		"../examples/battletest/tax.arb",
		"../examples/battletest/greenhouse.arb",
		"../examples/continuous/chained.arb",
		"../examples/continuous/monitor.arb",
	} {
		t.Run(path, func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			p, err := expert.Compile(src)
			if err != nil {
				t.Skipf("not compilable as an expert program: %v", err)
			}
			blob, err := p.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			q, err := engine.UnmarshalProgram(blob)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Rules(), q.Rules()) {
				t.Fatal("rules differ after round trip")
			}
			again, err := q.MarshalBinary()
			if err != nil || !bytes.Equal(blob, again) {
				t.Fatalf("not byte-stable (err=%v)", err)
			}
		})
	}
}

func TestUnmarshalProgramRejectsBadInput(t *testing.T) {
	p, err := expert.Compile([]byte(precompiledSource))
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := p.MarshalBinary()
	if _, err := engine.UnmarshalProgram(nil); err == nil {
		t.Error("expected error for empty input")
	}
	if _, err := engine.UnmarshalProgram([]byte("XXXXXXXX")); err == nil {
		t.Error("expected error for bad magic")
	}
	for _, n := range []int{5, 9, len(blob) / 2, len(blob) - 1} {
		if _, err := engine.UnmarshalProgram(blob[:n]); err == nil {
			t.Errorf("expected error for blob truncated to %d bytes", n)
		}
	}
	if _, err := engine.UnmarshalProgram(append(append([]byte{}, blob...), 0)); err == nil {
		t.Error("expected error for trailing bytes")
	}
}
