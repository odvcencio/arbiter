package gameai_test

import (
	"math"
	"testing"

	"m31labs.dev/arbiter/examples/gameai/arena"
	"m31labs.dev/arbiter/gameai"
	"m31labs.dev/arbiter/vm"
)

// wantEvalDigest is the digest of TestDigestOverManyEvaluations. It must be
// the same on every target (amd64, arm64, js/wasm). If a deliberate change to
// the engine or to arena.arb moves it, update it on one target and confirm
// the others match.
const wantEvalDigest = 0x34fbec4a4f787df0

// TestDigestOverManyEvaluations makes 60,000 decisions over random views of
// the toy game and folds every chosen rule, score and parameter value, and a
// sample of rendered traces, into one digest.
func TestDigestOverManyEvaluations(t *testing.T) {
	prog, err := gameai.Load(arena.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	brain, err := gameai.NewBrain[arena.View](prog, arena.Decisions)
	if err != nil {
		t.Fatal(err)
	}
	var ds []gameai.Decision[arena.View]
	for _, spec := range arena.Decisions {
		d, err := brain.Decision(spec.Tag)
		if err != nil {
			t.Fatal(err)
		}
		ds = append(ds, d)
	}
	rng := gameai.Rand{State: 2026}
	var mem gameai.Memory
	var tr gameai.Trace
	digest := uint64(0)
	const views = 20000
	for i := 0; i < views; i++ {
		var v arena.View
		v.Tick = int64(i)
		v.Self.HasOrb = rng.Intn(4) == 0
		v.Self.OrbDist = float64(rng.Intn(60000)) / 1000
		v.Self.Pressure = float64(rng.Intn(6))
		v.Self.PassOpen = rng.Intn(2) == 0
		v.Self.PassDist = rng.Float64() * 40
		v.Self.MarkThreat = float64(rng.Intn(5))
		v.Self.Stamina = float64(rng.Intn(101))
		v.Orb.Free = rng.Intn(3) == 0
		v.Team.ScoreDiff = float64(rng.Intn(9) - 4)
		v.Team.LostOrb = rng.Intn(2) == 0
		v.Team.TrailingTicks = float64(rng.Intn(5000))
		v.Mem = &mem
		for k, d := range ds {
			var trace *gameai.Trace
			if i%97 == 0 {
				trace = &tr
			}
			c, err := d.Explain(&v, rng.Uint64(), trace)
			if err != nil {
				t.Fatal(err)
			}
			digest = gameai.Mix(digest, uint64(c.RuleIndex()+1))
			digest = gameai.Mix(digest, math.Float64bits(c.Score()))
			for _, p := range c.Params() {
				digest = gameai.Mix(digest, gameai.Key(p.Key))
				digest = gameai.Mix(digest, uint64(p.Value.Typ))
				digest = gameai.Mix(digest, math.Float64bits(p.Value.Num))
				if p.Value.Typ == vm.TypeString {
					digest = gameai.Mix(digest, gameai.Key(c.Text(p.Key)))
				}
			}
			if trace != nil {
				digest = gameai.Mix(digest, gameai.Key(trace.String()))
			}
			if k == 2 && c.OK() {
				mem.Note(c.Text("kind"), v.Tick)
			}
		}
	}
	t.Logf("evaluation digest %#016x", digest)
	if digest != wantEvalDigest {
		t.Fatalf("evaluation digest %#016x, want %#016x", digest, uint64(wantEvalDigest))
	}
}
