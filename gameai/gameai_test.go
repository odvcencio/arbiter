package gameai_test

import (
	"strings"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle/wire"
	"m31labs.dev/arbiter/gameai"
	"m31labs.dev/arbiter/vm"
)

// load compiles source, writes it as a bundle and loads the bundle, the path a
// shipped game takes.
func load(t testing.TB, source string) *gameai.Program {
	t.Helper()
	compiled, err := arbiter.Compile([]byte(source))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	data, err := wire.Marshal(compiled.Ruleset)
	if err != nil {
		t.Fatal(err)
	}
	p, err := gameai.Load(data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}

type unit struct {
	Tick int64 `arb:"tick"`
	Self struct {
		HP    float64 `arb:"hp"`
		Ammo  int     `arb:"ammo"`
		Armed bool    `arb:"armed"`
		Role  string  `arb:"role"`
	} `arb:"self"`
	Enemy *struct {
		Dist float64 `arb:"dist"`
	} `arb:"enemy"`
	Roll float64       `arb:"roll"`
	Mem  gameai.Memory `arb:"mem"`
}

const unitRules = `
tag "act"
tag "pick"
tag "first"

rule Attack tag "act" {
	when { self.armed and enemy.dist < 10 }
	then Act { kind: "attack", score: 50 + self.ammo }
}
rule Flee tag "act" {
	when { self.hp < 30 }
	then Act { kind: "flee", score: 90 - self.hp }
}
rule Tie tag "act" {
	when { self.role == "tank" }
	then Act { kind: "tie", score: 60 }
}
rule Idle tag "act" {
	when { true }
	then Act { kind: "idle", score: 1 }
}

rule A tag "pick" { when { true } then Pick { kind: "a", weight: 1 } }
rule B tag "pick" { when { true } then Pick { kind: "b", weight: 3 } }
rule Z tag "pick" { when { true } then Pick { kind: "z", weight: 0 } }
rule N tag "pick" { when { true } then Pick { kind: "n" } }

rule Reload tag "first" {
	when { self.ammo == 0 and tick - mem.last.Reload >= 30 }
	then Do { kind: "reload" }
	otherwise Do { kind: "wait" }
}
rule Shoot tag "first" {
	when { self.ammo > 0 }
	then Do { kind: "shoot" }
}
`

func newUnitBrain(t testing.TB, p *gameai.Program) *gameai.Brain[unit] {
	t.Helper()
	b, err := gameai.NewBrain[unit](p, []gameai.Spec{
		{Tag: "act", Select: gameai.Best("score")},
		{Tag: "pick", Select: gameai.Weighted("weight")},
		{Tag: "first", Select: gameai.First()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decision(t testing.TB, b *gameai.Brain[unit], name string) gameai.Decision[unit] {
	t.Helper()
	d, err := b.Decision(name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestBestPicksHighestScoreAndBreaksTiesByDeclarationOrder(t *testing.T) {
	b := newUnitBrain(t, load(t, unitRules))
	act := decision(t, b, "act")

	var u unit
	u.Self.HP, u.Self.Ammo, u.Self.Armed = 80, 10, true
	u.Enemy = &struct {
		Dist float64 `arb:"dist"`
	}{Dist: 5}
	c, err := act.Decide(&u, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !c.OK() || c.Text("kind") != "attack" || c.Score() != 60 || c.Rule() != "Attack" {
		t.Fatalf("got %q score %v rule %q", c.Text("kind"), c.Score(), c.Rule())
	}

	// Attack scores 60 and Tie scores 60: the rule declared first wins.
	u.Self.Ammo, u.Self.Role = 10, "tank"
	c, _ = act.Decide(&u, 0)
	if c.Text("kind") != "attack" {
		t.Fatalf("tie went to %q, want attack", c.Text("kind"))
	}

	// A nil pointer reads as null, so enemy.dist < 10 is false.
	u.Enemy = nil
	u.Self.Role, u.Self.HP = "", 10
	c, _ = act.Decide(&u, 0)
	if c.Text("kind") != "flee" || c.Score() != 80 {
		t.Fatalf("got %q %v, want flee 80", c.Text("kind"), c.Score())
	}
}

func TestWeightedIsDeterministicAndProportional(t *testing.T) {
	p := load(t, unitRules)
	b := newUnitBrain(t, p)
	pick := decision(t, b, "pick")
	var u unit
	counts := map[string]int{}
	const draws = 40000
	for i := uint64(0); i < draws; i++ {
		seed := gameai.Seed(99, i, 7, gameai.Key("pick"))
		c, err := pick.Decide(&u, seed)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := pick.Decide(&u, seed)
		kind := c.Text("kind")
		if again.Text("kind") != kind {
			t.Fatalf("seed %d gave %q then %q", seed, kind, again.Text("kind"))
		}
		counts[kind]++
	}
	if counts["z"] != 0 || counts["n"] != 0 {
		t.Fatalf("zero or missing weights were drawn: %v", counts)
	}
	// B has weight 3 of 4: expect 75% within 1.5 points.
	if share := float64(counts["b"]) / draws; share < 0.735 || share > 0.765 {
		t.Fatalf("b share %.3f, want about 0.75 (%v)", share, counts)
	}

	// The draw maps seeds to candidates by fixed arithmetic: Unit(seed)*total.
	if c, _ := pick.Decide(&u, 0); c.Text("kind") != "a" {
		t.Fatalf("seed 0 (u=0) gave %q, want a", c.Text("kind"))
	}
	if c, _ := pick.Decide(&u, ^uint64(0)); c.Text("kind") != "b" {
		t.Fatalf("max seed gave %q, want b", c.Text("kind"))
	}
}

func TestFirstWithFallbackAndMemoryCooldown(t *testing.T) {
	b := newUnitBrain(t, load(t, unitRules))
	first := decision(t, b, "first")
	var u unit
	u.Tick = 100
	c, err := first.Decide(&u, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Text("kind") != "reload" || c.Fallback() {
		t.Fatalf("got %q fallback=%v, want reload", c.Text("kind"), c.Fallback())
	}
	u.Mem.Note(c.Rule(), u.Tick)

	// Inside the 30-tick cooldown the otherwise action runs.
	u.Tick = 120
	c, _ = first.Decide(&u, 0)
	if c.Text("kind") != "wait" || !c.Fallback() {
		t.Fatalf("got %q fallback=%v, want wait from otherwise", c.Text("kind"), c.Fallback())
	}

	// Restoring a snapshot taken before the reload brings the old memory back.
	var before unit
	before.Tick = 120
	if c, _ := first.Decide(&before, 0); c.Text("kind") != "reload" {
		t.Fatalf("fresh memory gave %q, want reload", c.Text("kind"))
	}
	u.Tick = 130
	if c, _ := first.Decide(&u, 0); c.Text("kind") != "reload" {
		t.Fatalf("after cooldown got %q, want reload", c.Text("kind"))
	}
}

func TestBrainRejectsRulesThatReadMissingFields(t *testing.T) {
	p := load(t, `
tag "act"
rule R tag "act" { when { self.hp > 1 and self.mana > 2 } then Act { kind: "x" } }
`)
	_, err := gameai.NewBrain[unit](p, []gameai.Spec{{Tag: "act", Select: gameai.First()}})
	if err == nil || !strings.Contains(err.Error(), "act: self.mana") {
		t.Fatalf("err = %v, want a missing self.mana error", err)
	}
	if _, err := gameai.NewBrain[unit](p, []gameai.Spec{{Tag: "act", Select: gameai.First()}}, gameai.AllowMissing()); err != nil {
		t.Fatalf("AllowMissing: %v", err)
	}
	if _, err := gameai.NewBrain[unit](p, []gameai.Spec{{Tag: "nope"}}); err == nil {
		t.Fatal("unknown tag accepted")
	}
}

func TestLoadRejectsWallClockAndGovernance(t *testing.T) {
	for name, src := range map[string]string{
		"window":   `rule R { active_from 2026-01-01T00:00:00Z when { true } then A {} }`,
		"rollout":  `rule R { rollout 50 when { true } then A {} }`,
		"segment":  "segment s { x > 1 }\nrule R { when segment s { true } then A {} }",
		"requires": "rule Q { when { true } then A {} }\nrule R { requires Q when { true } then A {} }",
	} {
		compiled, err := arbiter.Compile([]byte(src))
		if err != nil {
			t.Fatalf("%s: compile: %v", name, err)
		}
		if _, err := gameai.FromRuleset(compiled.Ruleset); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestKillSwitchOnLeavesRuleOut(t *testing.T) {
	p := load(t, `
tag "act"
rule Off tag "act" { kill_switch on when { true } then A { kind: "off" } }
rule On tag "act" { when { true } then A { kind: "on" } }
`)
	d, err := gameai.NewDecider(p, "act")
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.Decide(vm.DataFromMap(nil, p.Strings()), gameai.First(), 0, nil)
	if err != nil || c.Rule() != "On" {
		t.Fatalf("got %q, %v", c.Rule(), err)
	}
}

func TestSwapKeepsHandlesAndRejectsIncompatiblePrograms(t *testing.T) {
	p1 := load(t, unitRules)
	b := newUnitBrain(t, p1)
	act := decision(t, b, "act")
	var u unit
	u.Self.HP = 80
	if c, _ := act.Decide(&u, 0); c.Text("kind") != "idle" {
		t.Fatalf("before swap got %q", c.Text("kind"))
	}

	p2 := load(t, strings.Replace(unitRules, `kind: "idle", score: 1`, `kind: "patrol", score: 1`, 1))
	if err := b.Swap(p2); err != nil {
		t.Fatal(err)
	}
	if b.Program() != p2 || p1.Digest() == p2.Digest() {
		t.Fatal("swap did not install the new program")
	}
	if c, _ := act.Decide(&u, 0); c.Text("kind") != "patrol" {
		t.Fatalf("after swap got %q", c.Text("kind"))
	}

	bad := load(t, `
tag "act"
tag "pick"
tag "first"
rule R tag "act" { when { self.mana > 1 } then A {} }
rule P tag "pick" { when { true } then A {} }
rule F tag "first" { when { true } then A {} }
`)
	if err := b.Swap(bad); err == nil {
		t.Fatal("swap accepted a program that reads self.mana")
	}
	if b.Program() != p2 {
		t.Fatal("failed swap replaced the program")
	}
}

func TestDecideRejectsContextBoundToAnotherProgram(t *testing.T) {
	p1, p2 := load(t, unitRules), load(t, unitRules)
	d, err := gameai.NewDecider(p1, "act")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := gameai.Bind[unit](p2)
	if err != nil {
		t.Fatal(err)
	}
	var u unit
	if _, err := d.Decide(ctx.Set(&u), gameai.First(), 0, nil); err == nil {
		t.Fatal("decider accepted a context bound to another program")
	}
}

func TestStructContextMatchesMapContext(t *testing.T) {
	p := load(t, unitRules)
	ctx, err := gameai.Bind[unit](p)
	if err != nil {
		t.Fatal(err)
	}
	var u unit
	u.Tick, u.Self.HP, u.Self.Ammo, u.Self.Armed, u.Self.Role = 42, 12.5, 3, true, "runtime-only string"
	u.Mem.Note("Reload", 40)
	ctx.Set(&u)
	for key, want := range map[string]vm.Value{
		"tick":             vm.NumVal(42),
		"self.hp":          vm.NumVal(12.5),
		"self.ammo":        vm.NumVal(3),
		"self.armed":       vm.BoolVal(true),
		"enemy.dist":       vm.NullVal(),
		"mem.last.Reload":  vm.NumVal(40),
		"mem.count.Reload": vm.NumVal(1),
		"mem.last.Other":   vm.NumVal(gameai.Never),
		"nope":             vm.NullVal(),
	} {
		if got := ctx.Get(key); !got.Equal(want) {
			t.Errorf("%s = %+v, want %+v", key, got, want)
		}
	}
	if got := ctx.Get("self.role"); got.Typ != vm.TypeString || got.Any != "runtime-only string" {
		t.Errorf("self.role = %+v", got)
	}
	u.Self.Role = "tank" // a program constant reads as a pooled string
	if got := ctx.Get("self.role"); got.Typ != vm.TypeString || got.Any != nil {
		t.Errorf("self.role = %+v, want pooled", got)
	}
}
