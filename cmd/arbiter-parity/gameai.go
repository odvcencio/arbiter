package main

import (
	"fmt"
	"math"

	"m31labs.dev/arbiter/examples/gameai/arena"
	"m31labs.dev/arbiter/gameai"
)

// gameaiSection plays one minute (1,800 ticks at 30 Hz) of the toy game in
// examples/gameai/arena from its shipped bundle: 22 bots make three decisions
// each per tick (utility scoring with a decision table, a seeded weighted
// draw, and a rule with a cooldown in per-agent memory). Every 30 ticks it
// records the running decision digest, the score, one bot's position and
// memory, and a rendered decision trace.
func gameaiSection() ([]string, error) {
	prog, err := gameai.Load(arena.Bundle)
	if err != nil {
		return nil, err
	}
	m, err := arena.NewMatch(prog, 7)
	if err != nil {
		return nil, err
	}
	m.Watch = 4
	lines := []string{"bundle " + prog.DigestHex()}
	for tick := 0; tick < 1800; tick++ {
		if err := m.Step(); err != nil {
			return nil, err
		}
		if tick%30 != 29 {
			continue
		}
		b := m.Bots[m.Watch]
		mem, _ := b.Mem.MarshalBinary()
		lines = append(lines,
			fmt.Sprintf("tick %d digest %016x score %v holder %d bot %d,%d stamina %d mem %x", m.Tick, m.Digest, m.Score, m.Holder, b.X, b.Y, b.Stamina, mem),
			fmt.Sprintf("trace %q", m.Trace.String()))
	}
	for i := uint64(0); i < 16; i++ {
		seed := gameai.Seed(7, i, 3, gameai.Key("tactic"))
		lines = append(lines, fmt.Sprintf("seed %d %016x unit %016x", i, seed, math.Float64bits(gameai.Unit(seed))))
	}
	return lines, nil
}
