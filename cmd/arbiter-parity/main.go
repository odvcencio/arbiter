// Command arbiter-parity evaluates a fixed corpus of rules and prints a digest
// of every output, activation order and trace. The digest must be identical on
// every target (amd64 v1 and v3, arm64, js/wasm) and on every run. See
// scripts/parity.sh.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/expert"
	"m31labs.dev/arbiter/vm"
)

const (
	statelessSource = `
tag "add" tag "sub" tag "mul" tag "div" tag "mod" tag "abs" tag "min" tag "max" tag "chain" tag "float"

rule Add tag "add" { when { true } then Out { v: a + b } }
rule Sub tag "sub" { when { true } then Out { v: a - b } }
rule Mul tag "mul" { when { true } then Out { v: a * b } }
rule Div tag "div" { when { true } then Out { f: floor(a / b), c: ceil(a / b), r: round(a / b) } }
rule Mod tag "mod" { when { true } then Out { v: a % b } }
rule Abs tag "abs" { when { true } then Out { v: abs(a - b) } }
rule Min tag "min" { when { true } then Out { v: min(a, b) } }
rule Max tag "max" { when { true } then Out { v: max(a, b) } }
rule Chain tag "chain" {
	when { floor(a / 7) + b % 5 > -1000000 }
	then Out { v: (a * 3 + b) % 1000003, w: max(abs(a), min(b, 9)) - round(b / 4) }
}
rule FloatFMA tag "float" {
	when { true }
	then Out { v: a * x + y, w: a / x + y, s: x * y * a - y * a }
}
`
	expertIntSource = `
expert rule Summarize priority 30 {
	when { count(u.power for u in facts.Unit) > 0 }
	then assert Summary {
		key: "total",
		power: sum(u.power for u in facts.Unit),
		mean: floor(avg(u.cost for u in facts.Unit)),
		n: count(u.power for u in facts.Unit),
		weighted: sum(u.power * u.cost % 97 for u in facts.Unit),
	}
}

expert rule Hot priority 20 per_fact {
	when { any u in facts.Unit { u.power > 97 } }
	then emit Alert { key: u.key, p: u.power * u.cost % 1013, zone: u.zone }
}

expert rule PickOne priority 15 {
	when { any u in facts.Unit { u.power >= 90 and u.cost > 50 } }
	then emit Pick { unit: u.key, power: u.power, cost: u.cost }
}

expert rule PickRound priority 14 {
	when { any u in facts.Unit { u.zone == 3 and u.power % 7 == 0 } }
	then emit PickSeven { unit: u.key, power: u.power }
}

expert rule Tier priority 10 {
	when { any s in facts.Summary { s.power > 1000 } }
	then assert Tier { key: "t", level: floor(s.power / 1000), rest: s.power % 1000 }
}

expert rule Adjust priority 5 {
	requires Tier
	when { any t in facts.Tier { t.level > 0 } }
	then modify Unit {
		key: "u0007"
		set { power: 11 }
	}
}

expert rule Drop priority 4 {
	requires Tier
	when { any t in facts.Tier { t.level > 0 } }
	then retract Unit { key: "u0003" }
}
`
	expertFloatSource = `
expert rule FloatSum priority 20 {
	when { count(r.v for r in facts.Reading) > 0 }
	then assert FloatTotal {
		key: "t",
		sum: sum(r.v for r in facts.Reading),
		avg: avg(r.v for r in facts.Reading),
		mix: sum(r.v * 1.0000001 + r.w for r in facts.Reading),
	}
}

expert rule FloatPick priority 10 {
	when { any r in facts.Reading { r.v > 1000 } }
	then emit FloatPick { key: r.key, v: r.v }
}
`
	unitCount    = 2400
	readingCount = 2000
)

type rng uint64

func (r *rng) next() uint64 {
	x := uint64(*r)
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	*r = rng(x)
	return x
}

// bits renders a value so any one-bit difference in a float shows in the dump.
func bits(v any) string {
	switch x := v.(type) {
	case float64:
		return fmt.Sprintf("%016x", math.Float64bits(x))
	default:
		return fmt.Sprint(v)
	}
}

func renderParams(p map[string]any) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%s", k, bits(p[k]))
	}
	return b.String()
}

func statelessSection(profile vm.NumericProfile) ([]string, error) {
	program, err := arbiter.Compile([]byte(statelessSource))
	if err != nil {
		return nil, err
	}
	var lines []string
	pool := vm.NewStringPool(program.Ruleset.Constants.Strings())
	tags := []string{"add", "sub", "mul", "div", "mod", "abs", "min", "max", "chain"}
	if profile == vm.NumericDefault {
		tags = append(tags, "float")
	}
	inputs := []float64{0, 1, -1, 2, 3, -3, 7, -7, 10, 99, -99, 1000, 123456, -123456, 94906267, -94906267, 1 << 26, 1<<53 - 1, -(1<<53 - 1), 1 << 53}
	r := rng(0x9E3779B97F4A7C15)
	for i := 0; i < 40; i++ {
		inputs = append(inputs, float64(int64(r.next()%(1<<40))-(1<<39)))
	}
	for _, tag := range tags {
		prepared, err := vm.NewPreparedEvaluator(program.Ruleset, pool, []string{tag})
		if err != nil {
			return nil, err
		}
		prepared.SetNumericProfile(profile)
		for _, a := range inputs {
			for _, b := range inputs {
				data := map[string]any{"a": a, "b": b, "x": b, "y": a}
				if tag == "float" {
					// Fractions with long mantissas, so a fused multiply-add would differ.
					data = map[string]any{
						"a": a/3 + 0.1, "x": b/7 + 0.3, "y": -(a * b / 11),
					}
				}
				got, err := prepared.Eval(vm.DataFromMap(data, pool))
				if err != nil {
					lines = append(lines, fmt.Sprintf("%s a=%s b=%s error: %v", tag, bits(a), bits(b), err))
					continue
				}
				for _, m := range got {
					lines = append(lines, fmt.Sprintf("%s a=%s b=%s %s%s", tag, bits(a), bits(b), m.Name, renderParams(m.Params)))
				}
			}
		}
	}
	return lines, nil
}

func unitFacts() []expert.Fact {
	r := rng(0xD1B54A32D192ED03)
	facts := make([]expert.Fact, 0, unitCount)
	for i := 0; i < unitCount; i++ {
		key := fmt.Sprintf("u%04d", i)
		facts = append(facts, expert.Fact{Type: "Unit", Key: key, Fields: map[string]any{
			"power": float64(r.next() % 100),
			"cost":  float64(r.next()%200 + 1),
			"zone":  float64(r.next() % 8),
		}})
	}
	return facts
}

func readingFacts() []expert.Fact {
	r := rng(0xA24BAED4963EE407)
	facts := make([]expert.Fact, 0, readingCount)
	for i := 0; i < readingCount; i++ {
		// Wide dynamic range, so the float sum depends on the order of addition.
		mag := math.Pow(10, float64(int(r.next()%9))-3)
		v := (float64(r.next()%1000003) / 1000.7) * mag
		w := float64(r.next()%99991) / 3.3
		facts = append(facts, expert.Fact{Type: "Reading", Key: fmt.Sprintf("r%04d", i), Fields: map[string]any{"v": v, "w": w}})
	}
	return facts
}

func expertSection(source string, facts []expert.Fact, profile vm.NumericProfile) ([]string, error) {
	program, err := expert.Compile([]byte(source))
	if err != nil {
		return nil, err
	}
	if profile == vm.NumericInteger {
		if v := program.NumericProfileViolations(); len(v) > 0 {
			return nil, fmt.Errorf("corpus breaks the integer profile: %v", v)
		}
	}
	fixed := time.Unix(1_700_000_000, 0).UTC()
	session := expert.NewSession(program, map[string]any{}, facts, expert.Options{
		BundleID:       "parity",
		MaxRounds:      64,
		MaxMutations:   100000,
		Now:            func() time.Time { return fixed },
		NumericProfile: profile,
	})
	res, err := session.Run(context.Background())
	if err != nil {
		return nil, err
	}
	blob, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	lines := []string{fmt.Sprintf("rounds=%d mutations=%d stop=%s", res.Rounds, res.Mutations, res.StopReason)}
	for _, o := range res.Outcomes {
		lines = append(lines, fmt.Sprintf("outcome %s %s%s", o.Rule, o.Name, renderParams(o.Params)))
	}
	for _, a := range res.Activations {
		lines = append(lines, fmt.Sprintf("activation r%d %s %s %s changed=%v%s detail=%q trace=%d", a.Round, a.Rule, a.Kind, a.Target, a.Changed, renderParams(a.Params), a.Detail, len(a.Arbitrace)))
	}
	for _, f := range res.Facts {
		if f.Type != "Unit" {
			lines = append(lines, fmt.Sprintf("fact %s/%s%s", f.Type, f.Key, renderParams(f.Fields)))
		}
	}
	sum := sha256.Sum256(blob)
	lines = append(lines, "result-json-sha256 "+hex.EncodeToString(sum[:]))
	return lines, nil
}

func digest(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

func run(dump string) error {
	type section struct {
		name  string
		lines []string
	}
	var sections []section
	add := func(name string, lines []string, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		sections = append(sections, section{name, lines})
		return nil
	}
	l, err := statelessSection(vm.NumericInteger)
	if err := add("stateless-integer", l, err); err != nil {
		return err
	}
	l, err = statelessSection(vm.NumericDefault)
	if err := add("stateless-float", l, err); err != nil {
		return err
	}
	l, err = expertSection(expertIntSource, unitFacts(), vm.NumericInteger)
	if err := add("expert-integer", l, err); err != nil {
		return err
	}
	l, err = expertSection(expertFloatSource, readingFacts(), vm.NumericDefault)
	if err := add("expert-float", l, err); err != nil {
		return err
	}

	var all []string
	var full strings.Builder
	for _, s := range sections {
		d := digest(s.lines)
		fmt.Printf("%s %s\n", s.name, d)
		all = append(all, s.name+" "+d)
		fmt.Fprintf(&full, "== %s (%d lines)\n%s\n", s.name, len(s.lines), strings.Join(s.lines, "\n"))
	}
	fmt.Printf("all %s\n", digest(all))
	if dump != "" {
		return os.WriteFile(dump, []byte(full.String()), 0o644)
	}
	return nil
}

func main() {
	dump := flag.String("dump", "", "write the full corpus output to this file")
	flag.Parse()
	if err := run(*dump); err != nil {
		fmt.Fprintln(os.Stderr, "arbiter-parity:", err)
		os.Exit(1)
	}
}
