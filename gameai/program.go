// Package gameai runs precompiled Arbiter bundles as CPU decision logic inside
// a deterministic, fixed-step game simulation.
//
// The package loads binary bundles (the output of `arbiter bundle`) and
// evaluates them without the .arb parser or gotreesitter, so the same code runs
// on a game server and in a js/wasm browser client. For the same bundle and the
// same inputs it produces the same decisions on amd64, arm64 and js/wasm.
//
// See docs/game-ai.md for the guide and a toy example.
package gameai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"m31labs.dev/arbiter/bundle/wire"
	"m31labs.dev/arbiter/bytecode"
	"m31labs.dev/arbiter/vm"
)

// Program is one loaded bundle. It is immutable and safe for concurrent use;
// share one Program between every room, agent and goroutine that runs it.
type Program struct {
	rs     *bytecode.CompiledRuleset
	pool   *vm.StringPool
	digest [sha256.Size]byte
	rules  []string
}

// Load decodes a binary bundle. The digest of a Program loaded this way is the
// SHA-256 of data, so a server and a client that load the same file agree on
// it.
func Load(data []byte) (*Program, error) {
	rs, err := wire.Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("gameai: load bundle: %w", err)
	}
	return newProgram(rs, sha256.Sum256(data))
}

// FromRuleset wraps a ruleset compiled in process, for example by a server in
// development mode that compiles .arb source directly. The digest is the
// SHA-256 of the ruleset's wire encoding, which equals the digest of a bundle
// written without name hashing (`arbiter bundle --keep-names`).
func FromRuleset(rs *bytecode.CompiledRuleset) (*Program, error) {
	if rs == nil {
		return nil, fmt.Errorf("gameai: nil ruleset")
	}
	data, err := wire.Marshal(rs)
	if err != nil {
		return nil, fmt.Errorf("gameai: encode ruleset: %w", err)
	}
	return newProgram(rs, sha256.Sum256(data))
}

func newProgram(rs *bytecode.CompiledRuleset, digest [sha256.Size]byte) (*Program, error) {
	if rs.Constants == nil {
		return nil, fmt.Errorf("gameai: ruleset has no constant pool")
	}
	if err := checkRuntimeSafe(rs); err != nil {
		return nil, err
	}
	p := &Program{
		rs:     rs,
		pool:   vm.NewStringPool(rs.Constants.Strings()),
		digest: digest,
		rules:  make([]string, len(rs.Rules)),
	}
	if err := p.pool.Err(); err != nil {
		return nil, fmt.Errorf("gameai: %w", err)
	}
	for i, rule := range rs.Rules {
		p.rules[i] = rs.Constants.GetString(rule.NameIdx)
	}
	return p, nil
}

// checkRuntimeSafe rejects rule features whose meaning depends on state the
// game runtime does not have: the wall clock (active windows) and governed
// evaluation (segments, prerequisites, exclusions and rollouts). Accepting them
// would make a rule behave differently here than in governed evaluation, or
// differently on two machines.
func checkRuntimeSafe(rs *bytecode.CompiledRuleset) error {
	for _, rule := range rs.Rules {
		name := rs.Constants.GetString(rule.NameIdx)
		switch {
		case rule.HasActiveFrom || rule.HasActiveUntil:
			return fmt.Errorf("gameai: rule %s has an active window; windows read the wall clock, which differs between machines (pass the simulation tick as an input instead)", name)
		case rule.HasSegment:
			return fmt.Errorf("gameai: rule %s uses a segment gate, which needs governed evaluation (move the condition into the rule)", name)
		case rule.PrereqLen > 0 || rule.ExcludeLen > 0:
			return fmt.Errorf("gameai: rule %s uses requires or excludes, which need governed evaluation", name)
		case rule.HasRollout:
			return fmt.Errorf("gameai: rule %s has a rollout, which needs governed evaluation (use weighted selection instead)", name)
		}
	}
	return nil
}

// Digest identifies the bundle. Record it with snapshots and replays, and
// check that every peer in a match runs the same digest.
func (p *Program) Digest() [sha256.Size]byte { return p.digest }

// DigestHex is Digest as lowercase hexadecimal.
func (p *Program) DigestHex() string { return hex.EncodeToString(p.digest[:]) }

// Rules returns the rule names in declaration order. Bundles written with name
// hashing hold hashes here. The caller must not modify the slice.
func (p *Program) Rules() []string { return p.rules }

// Ruleset returns the compiled ruleset. The caller must not modify it.
func (p *Program) Ruleset() *bytecode.CompiledRuleset { return p.rs }

// Strings returns the program's string pool, which resolves the string
// constants in evaluation results.
func (p *Program) Strings() *vm.StringPool { return p.pool }
