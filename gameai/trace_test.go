package gameai_test

import (
	"testing"

	"m31labs.dev/arbiter/gameai"
)

func TestTraceText(t *testing.T) {
	p := load(t, `
tag "act"
rule Attack tag "act" {
	when { self.armed and enemy.dist < 10 and self.ammo * 2 >= 4 }
	then Act { kind: "attack", score: 50 + self.ammo }
}
rule Flee tag "act" {
	when { self.hp between [0, 30] }
	then Act { kind: "flee", score: 90 - self.hp }
}
rule Taunt tag "act" {
	when { self.role in ["tank", "brawler"] }
	then Act { kind: "taunt", score: 10 }
	otherwise Act { kind: "idle", score: 1 }
}
`)
	b, err := gameai.NewBrain[unit](p, []gameai.Spec{{Name: "combat", Tag: "act", Select: gameai.Best("score")}})
	if err != nil {
		t.Fatal(err)
	}
	act, _ := b.Decision("combat")
	var u unit
	u.Self.Armed, u.Self.Ammo, u.Self.HP, u.Self.Role = true, 1, 40, "scout"
	u.Enemy = &struct {
		Dist float64 `arb:"dist"`
	}{Dist: 4}
	var tr gameai.Trace
	c, err := act.Explain(&u, 0, &tr)
	if err != nil {
		t.Fatal(err)
	}
	want := `combat best(score) -> Taunt 1
- Attack: self.armed=true enemy.dist(4) < 10 (self.ammo * 2)(2) >= 4 NO
- Flee: self.hp(40) between [0, 30] NO
> Taunt 1: self.role("scout") in ["tank", "brawler"] NO
`
	if got := tr.String(); got != want {
		t.Fatalf("trace:\n%s\nwant:\n%s", got, want)
	}
	if !c.Fallback() || tr.Chosen != 0 || len(tr.Candidates) != 1 || tr.Candidates[0].Score != 1 {
		t.Fatalf("choice %v, candidates %+v", c.Rule(), tr.Candidates)
	}

	// The same trace is reused for the next decision and does not allocate.
	u.Self.Ammo = 5
	allocs := testing.AllocsPerRun(200, func() {
		if _, err := act.Explain(&u, 0, &tr); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("%.1f allocations per traced decision, want 0", allocs)
	}
	if tr.Chosen < 0 || tr.Candidates[tr.Chosen].Rule != "Attack" {
		t.Fatalf("after reuse chose %+v", tr.Candidates)
	}
}

func TestWeightedTraceShowsTheDraw(t *testing.T) {
	b := newUnitBrain(t, load(t, unitRules))
	pick := decision(t, b, "pick")
	var u unit
	var tr gameai.Trace
	c, err := pick.Explain(&u, ^uint64(0), &tr)
	if err != nil {
		t.Fatal(err)
	}
	want := "pick weighted(weight) -> B 3 (draw 3.9999999999999996 of 4)\n+ A 1:\n> B 3:\n+ Z:\n+ N:\n"
	if got := tr.String(); got != want || c.Rule() != "B" {
		t.Fatalf("trace:\n%q\nwant:\n%q", got, want)
	}
}
