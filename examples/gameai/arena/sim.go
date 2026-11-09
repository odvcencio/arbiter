package arena

import (
	"fmt"
	"math"

	"m31labs.dev/arbiter/gameai"
)

// View is what one bot sees each tick. Its arb tags are the names arena.arb
// reads; gameai.Bind maps them to fields once, so building a View and
// deciding does not allocate.
type View struct {
	Tick int64 `arb:"tick"`
	Self struct {
		HasOrb     bool    `arb:"has_orb"`
		OrbDist    float64 `arb:"orb_dist"`
		Pressure   float64 `arb:"pressure"`
		PassOpen   bool    `arb:"pass_open"`
		PassDist   float64 `arb:"pass_dist"`
		MarkThreat float64 `arb:"mark_threat"`
		Stamina    float64 `arb:"stamina"`
	} `arb:"self"`
	Orb struct {
		Free bool `arb:"free"`
	} `arb:"orb"`
	Team struct {
		ScoreDiff     float64 `arb:"score_diff"`
		LostOrb       bool    `arb:"lost_orb"`
		TrailingTicks float64 `arb:"trailing_ticks"`
	} `arb:"team"`
	// Mem points at the bot's memory, which lives in the match state.
	Mem *gameai.Memory `arb:"mem"`
}

// Field size and movement, in integer units (1 unit = 1 mm). Integer
// positions keep the toy simulation itself trivially deterministic; only the
// decisions use floating point, inside the VM.
const (
	Bots      = 22
	PerTeam   = Bots / 2
	FieldX    = 100_000
	FieldY    = 60_000
	Speed     = 200 // units per tick, 6 m/s at 30 Hz
	DashTicks = 15
	reach     = 1_000
)

// Bot is one player's simulation state.
type Bot struct {
	X, Y      int64
	Stamina   int64
	DashLeft  int64
	Intent    uint8
	Mem       gameai.Memory
	LastScore float64
}

// State is the whole simulation state. It is a plain value: copying it is a
// snapshot, assigning it back is a restore.
type State struct {
	Tick     int64
	OrbX     int64
	OrbY     int64
	Holder   int // bot index, or -1 when the orb is free
	Score    [2]int64
	Trailing [2]int64
	Bots     [Bots]Bot
	Digest   uint64 // running hash of every decision
}

// Intent codes stored in Bot.Intent.
const (
	IntentHold uint8 = iota
	IntentChase
	IntentCarry
	IntentPass
	IntentMark
)

var intentCodes = map[string]uint8{"hold": IntentHold, "chase": IntentChase, "carry": IntentCarry, "pass": IntentPass, "mark": IntentMark}

// Match runs one match of the toy game.
type Match struct {
	State
	Seed    uint64
	brain   *gameai.Brain[View]
	intent  gameai.Decision[View]
	tactic  gameai.Decision[View]
	ability gameai.Decision[View]
	view    View
	// Watch, when in range, is the bot whose intent decision is traced into
	// Trace every tick, for a debug overlay.
	Watch int
	Trace gameai.Trace
}

// Decisions is the brain layout arena.arb is written for.
var Decisions = []gameai.Spec{
	{Tag: "intent", Select: gameai.Best("score")},
	{Tag: "tactic", Select: gameai.Weighted("weight")},
	{Tag: "ability", Select: gameai.First()},
}

// NewMatch starts a match with the given program and seed.
func NewMatch(prog *gameai.Program, seed uint64) (*Match, error) {
	brain, err := gameai.NewBrain[View](prog, Decisions)
	if err != nil {
		return nil, err
	}
	m := &Match{Seed: seed, brain: brain, Watch: -1}
	for _, d := range []struct {
		name string
		dst  *gameai.Decision[View]
	}{{"intent", &m.intent}, {"tactic", &m.tactic}, {"ability", &m.ability}} {
		if *d.dst, err = brain.Decision(d.name); err != nil {
			return nil, err
		}
	}
	m.kickoff()
	return m, nil
}

// Views returns what every bot sees at the current tick.
func (m *Match) Views() []View {
	views := make([]View, Bots)
	for i := range views {
		m.fillView(i, &views[i])
	}
	return views
}

// Brain returns the match's brain, for hot reload with Swap.
func (m *Match) Brain() *gameai.Brain[View] { return m.brain }

func (m *Match) kickoff() {
	m.OrbX, m.OrbY, m.Holder = FieldX/2, FieldY/2, -1
	for i := range m.Bots {
		b := &m.Bots[i]
		team, slot := int64(i/PerTeam), int64(i%PerTeam)
		b.X = 10_000 + (slot%4)*8_000
		if team == 1 {
			b.X = FieldX - b.X
		}
		b.Y = 6_000 + (slot/4)*20_000 + (slot%4)*1_500
		if b.Stamina == 0 {
			b.Stamina = 100
		}
		b.DashLeft = 0
	}
}

// Step advances the match by one fixed tick: every bot makes its three
// decisions, then the world moves.
func (m *Match) Step() error {
	for i := range m.Bots {
		if err := m.decide(i); err != nil {
			return fmt.Errorf("tick %d bot %d: %w", m.Tick, i, err)
		}
	}
	m.move()
	m.Tick++
	return nil
}

func (m *Match) decide(i int) error {
	b := &m.Bots[i]
	v := &m.view
	m.fillView(i, v)

	var tr *gameai.Trace
	if i == m.Watch {
		tr = &m.Trace
	}
	intent, err := m.intent.Explain(v, 0, tr)
	if err != nil {
		return err
	}
	b.Intent = IntentHold
	b.LastScore = 0
	if intent.OK() {
		b.Intent = intentCodes[intent.Text("kind")]
		b.LastScore = intent.Score()
		m.record(i, intent)
	}

	seed := gameai.Seed(m.Seed, uint64(m.Tick), uint64(i), gameai.Key("tactic"))
	tactic, err := m.tactic.Decide(v, seed)
	if err != nil {
		return err
	}
	if tactic.OK() {
		m.record(i, tactic)
		if tactic.Text("kind") == "press" && b.Intent == IntentHold {
			b.Intent = IntentMark
		}
	}

	ability, err := m.ability.Decide(v, 0)
	if err != nil {
		return err
	}
	if ability.OK() {
		m.record(i, ability)
		b.Mem.Note(ability.Rule(), m.Tick)
		b.Stamina -= int64(ability.Number("cost"))
		if ability.Text("kind") == "dash" {
			b.DashLeft = DashTicks
		} else if m.Holder >= 0 && team(m.Holder) != team(i) && dist(b.X, b.Y, m.OrbX, m.OrbY) <= 2*reach {
			m.Holder = -1 // shove knocks the orb loose
		}
	}
	return nil
}

// record folds one decision into the running digest.
func (m *Match) record(bot int, c gameai.Choice) {
	h := gameai.Mix(m.Digest, uint64(m.Tick))
	h = gameai.Mix(h, uint64(bot))
	h = gameai.Mix(h, uint64(c.RuleIndex()))
	m.Digest = gameai.Mix(h, math.Float64bits(c.Score()))
}

func (m *Match) fillView(i int, v *View) {
	b := &m.Bots[i]
	t := team(i)
	v.Tick = m.Tick
	v.Mem = &b.Mem
	v.Self.HasOrb = m.Holder == i
	v.Self.OrbDist = float64(dist(b.X, b.Y, m.OrbX, m.OrbY)) / 1000
	v.Self.Stamina = float64(b.Stamina)
	v.Orb.Free = m.Holder < 0
	v.Team.ScoreDiff = float64(m.Score[t] - m.Score[1-t])
	v.Team.LostOrb = m.Holder >= 0 && team(m.Holder) != t
	v.Team.TrailingTicks = float64(m.Trailing[t])

	pressure, threat := 0, 0
	for j := range m.Bots {
		if team(j) == t {
			continue
		}
		d := dist(b.X, b.Y, m.Bots[j].X, m.Bots[j].Y)
		if d <= 5*reach {
			pressure++
		}
		if d <= 8*reach {
			threat++
		}
	}
	v.Self.Pressure = float64(pressure)
	v.Self.MarkThreat = 0
	if v.Team.LostOrb {
		v.Self.MarkThreat = float64(threat)
	}

	v.Self.PassOpen, v.Self.PassDist = false, 0
	if v.Self.HasOrb {
		if j := m.passTarget(i); j >= 0 {
			v.Self.PassOpen = true
			v.Self.PassDist = float64(dist(b.X, b.Y, m.Bots[j].X, m.Bots[j].Y)) / 1000
		}
	}
}

// passTarget returns the open teammate closest to the goal ahead, or -1.
func (m *Match) passTarget(i int) int {
	t := team(i)
	best, bestX := -1, int64(0)
	for j := range m.Bots {
		if j == i || team(j) != t {
			continue
		}
		x := m.Bots[j].X
		if t == 1 {
			x = FieldX - x
		}
		if best >= 0 && x <= bestX {
			continue
		}
		open := true
		for k := range m.Bots {
			if team(k) != t && dist(m.Bots[j].X, m.Bots[j].Y, m.Bots[k].X, m.Bots[k].Y) <= 3*reach {
				open = false
				break
			}
		}
		if open {
			best, bestX = j, x
		}
	}
	return best
}

func (m *Match) move() {
	for i := range m.Bots {
		b := &m.Bots[i]
		t := team(i)
		goalX := int64(FieldX)
		if t == 1 {
			goalX = 0
		}
		tx, ty := b.X, b.Y
		switch b.Intent {
		case IntentChase:
			tx, ty = m.OrbX, m.OrbY
		case IntentCarry:
			tx, ty = goalX, FieldY/2
		case IntentPass:
			if j := m.passTarget(i); j >= 0 && m.Holder == i {
				m.Holder = j
			}
		case IntentMark:
			if m.Holder >= 0 {
				tx, ty = m.Bots[m.Holder].X, m.Bots[m.Holder].Y
			}
		}
		speed := int64(Speed)
		if b.DashLeft > 0 {
			speed *= 2
			b.DashLeft--
		}
		b.X = clamp(step(b.X, tx, speed), 0, FieldX)
		b.Y = clamp(step(b.Y, ty, speed), 0, FieldY)
		if b.Stamina < 100 && m.Tick%10 == 0 {
			b.Stamina++
		}
	}
	if m.Holder >= 0 {
		h := &m.Bots[m.Holder]
		m.OrbX, m.OrbY = h.X, h.Y
		if (team(m.Holder) == 0 && h.X >= FieldX) || (team(m.Holder) == 1 && h.X <= 0) {
			m.Score[team(m.Holder)]++
			m.kickoff()
		}
	} else {
		for i := range m.Bots {
			if dist(m.Bots[i].X, m.Bots[i].Y, m.OrbX, m.OrbY) <= reach {
				m.Holder = i
				break
			}
		}
	}
	for t := range m.Trailing {
		if m.Score[t] < m.Score[1-t] {
			m.Trailing[t]++
		} else {
			m.Trailing[t] = 0
		}
	}
}

func team(i int) int { return i / PerTeam }

func dist(ax, ay, bx, by int64) int64 { return max(abs(ax-bx), abs(ay-by)) }

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func step(from, to, speed int64) int64 {
	switch {
	case to > from+speed:
		return from + speed
	case to < from-speed:
		return from - speed
	}
	return to
}

func clamp(x, lo, hi int64) int64 { return min(max(x, lo), hi) }
