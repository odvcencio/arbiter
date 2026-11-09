# Game AI with Arbiter

Arbiter can run the CPU players in a deterministic, fixed-step game. You write
decisions as `.arb` rules, compile them to a small binary bundle, and the game
evaluates that bundle every tick, on the server and in the browser, with
identical results. Package `gameai` is the runtime: it does not link the `.arb`
parser, it builds for `js/wasm` (and TinyGo), and once warm a tick allocates
nothing.

This guide walks through a toy team game that ships with the repository,
[`examples/gameai/arena`](../examples/gameai/arena): two teams of 11 bots chase
one orb and try to carry it into the other goal. Each tick every bot makes
three decisions.

| Decision | Kind | How the choice is made |
|---|---|---|
| `intent` | utility scoring | every matching rule is a candidate with a score; the highest score wins |
| `tactic` | weighted random | candidates are drawn in proportion to a weight, from a seed |
| `ability` | priority list | the first matching rule wins; cooldowns come from per-agent memory |

## Contents

- [1. Write the rules](#1-write-the-rules)
- [2. Build the bundle](#2-build-the-bundle)
- [3. Describe what an agent sees](#3-describe-what-an-agent-sees)
- [4. Decide every tick](#4-decide-every-tick)
- [Selections: first, best, weighted](#selections-first-best-weighted)
- [Decision tables](#decision-tables)
- [Memory and cooldowns](#memory-and-cooldowns)
- [Randomness](#randomness)
- [Determinism](#determinism)
- [Performance](#performance)
- [Debug traces](#debug-traces)
- [Hot reload and versions](#hot-reload-and-versions)
- [Browser builds](#browser-builds)
- [Limits](#limits)

## 1. Write the rules

A decision is a set of rules that share a tag. Each rule that matches is one
candidate; its `then` action carries the parameters the game reads. This is
part of [`arena.arb`](../examples/gameai/arena/arena.arb):

```arb
tag "intent"
tag "tactic"
tag "ability"

rule Chase tag "intent" {
    when { not self.has_orb and orb.free }
    then Intent { kind: "chase", score: 80 - self.orb_dist * 2 }
}

rule Carry tag "intent" {
    when { self.has_orb }
    then Intent { kind: "carry", score: 100 - self.pressure * 10 }
}

rule Press tag "tactic" {
    when { team.score_diff < 0 }
    then Tactic { kind: "press", weight: 3 + team.trailing_ticks / 300 }
}

rule Balanced tag "tactic" {
    when { true }
    then Tactic { kind: "balanced", weight: 4 }
}

rule Dash tag "ability" {
    when { tick - mem.last.dash >= 90 and self.stamina >= 30 and self.orb_dist < 6 }
    then Ability { kind: "dash", cost: 30 }
}
```

Rules are plain Arbiter rules: comparisons, arithmetic, `let` bindings, table
lookups, `otherwise` actions and `kill_switch on` (which leaves a rule out)
all work. What a game runtime cannot honor is rejected when the bundle loads:
active windows (they read the wall clock), segment gates, `requires` and
`excludes`, and rollouts (they need governed evaluation). Pass the tick as an
input instead of using time, and use a weighted decision instead of a rollout.

## 2. Build the bundle

```sh
arbiter bundle ai/arena.arb --game --keep-names -o assets/arena.arbb
```

`--game` checks that the rules can run in the game runtime and skips the edge
export risk policy, because a game ships its decision logic to clients on
purpose. The output is deterministic: the same source always gives the same
bytes, so a build system can cache it. The command prints the rule count, the
decisions (tags) it found and the SHA-256 of the bundle.

`--keep-names` keeps rule names readable for traces and `Choice.Rule`. Without
it rule names become hashes; action names, parameter keys and every string the
rules compare or return stay readable. If you ship hashed bundles, key memory
by an action parameter (as the arena does with `kind`), not by rule name.

A Go build tool (for example a `gosx build` step) can skip the CLI and call the
same code:

```go
import "m31labs.dev/arbiter/gameai/build"

res, err := build.CompileFile("ai/arena.arb", build.Options{KeepNames: true})
// res.Bundle is the file to write, res.Digest its SHA-256,
// res.Tags the decisions it contains.
```

`gameai/build` links the parser; only `gameai` belongs in the game binary.

## 3. Describe what an agent sees

The game builds one view struct per agent per tick. `arb` struct tags map its
fields to the names the rules read; a tag on a nested struct is a prefix.

```go
type View struct {
	Tick int64 `arb:"tick"`
	Self struct {
		HasOrb   bool    `arb:"has_orb"`
		OrbDist  float64 `arb:"orb_dist"`
		Pressure float64 `arb:"pressure"`
		Stamina  float64 `arb:"stamina"`
	} `arb:"self"`
	Orb struct {
		Free bool `arb:"free"`
	} `arb:"orb"`
	Team struct {
		ScoreDiff     float64 `arb:"score_diff"`
		TrailingTicks float64 `arb:"trailing_ticks"`
	} `arb:"team"`
	Mem *gameai.Memory `arb:"mem"` // per-agent memory, lives in the sim state
}
```

Supported field types are `bool`, integers, floats, strings, pointers to them
(a nil pointer reads as null), nested structs and `gameai.Memory`. The binding
is resolved once per bundle, so reading a field during evaluation is a direct
memory load.

The brain checks the binding when it is created: if a rule reads a name the
struct does not provide (a typo such as `self.stamnia`), `NewBrain` fails with
the decision and the name, instead of the rule silently never firing. Pass
`gameai.AllowMissing()` to let missing names read as null.

## 4. Decide every tick

```go
prog, err := gameai.Load(arenaBundle) // the .arbb bytes, embedded or fetched
brain, err := gameai.NewBrain[View](prog, []gameai.Spec{
	{Tag: "intent", Select: gameai.Best("score")},
	{Tag: "tactic", Select: gameai.Weighted("weight")},
	{Tag: "ability", Select: gameai.First()},
})
intent, _ := brain.Decision("intent")
tactic, _ := brain.Decision("tactic")
ability, _ := brain.Decision("ability")

// In the fixed-step update, for each agent i:
fillView(i, &view)

c, err := intent.Decide(&view, 0)
if c.OK() {
	bot.Intent = c.Text("kind") // "chase", "carry", ...
}

seed := gameai.Seed(matchSeed, uint64(tick), uint64(i), gameai.Key("tactic"))
c, err = tactic.Decide(&view, seed)

c, err = ability.Decide(&view, 0)
if c.OK() {
	bot.Mem.Note(c.Text("kind"), tick) // starts the cooldown
	bot.Stamina -= int64(c.Number("cost"))
}
```

A `Choice` points into the decision's buffers: read what you need before
deciding the same decision again. `Text`, `Number` and `Bool` read action
parameters; `Rule`, `Action` and `Score` describe the candidate.

One `Program` is immutable and can be shared by every room and goroutine. A
`Brain` belongs to one simulation goroutine. The full game loop is in
[`sim.go`](../examples/gameai/arena/sim.go).

For lower-level control, `gameai.NewDecider(prog, tag)` evaluates one tag
against any `vm.DataContext`, and `gameai.Bind[T](prog)` gives the bound
struct context that `Brain` uses.

## Selections: first, best, weighted

The candidates of a decision are its matching rules in declaration order. A
rule whose condition fails but that has an `otherwise` action is a candidate
with that action (`Choice.Fallback` reports it).

- `gameai.First()` picks the first candidate, like a priority list.
- `gameai.Best(key)` picks the candidate whose parameter `key` is highest.
  Ties go to the rule declared first. Candidates whose score is missing, not a
  number or NaN are skipped; negative scores are fine.
- `gameai.Weighted(key)` draws a candidate with probability proportional to
  its parameter `key`. Weights that are missing, not numbers, zero, negative,
  NaN or infinite take no part. The draw is `Unit(seed) * total`, compared with
  the running sum of weights in declaration order, so the same seed picks the
  same candidate on every target.

Utility scoring is just arithmetic in the score expression, so curves, clamps
(`min`, `max`) and weights from tables all compose.

## Decision tables

A `table` holds data, and `lookup` selects a row inside `when` or `then`:

```arb
table spacing {
    lead_min: number | lead_max: number | spread: number | press: number
    -99              | -2               | 4              | 9
    -1               | 1                | 6              | 6
    2                | 99               | 9              | 3
}

rule Hold tag "intent" {
    when { true }
    then Intent {
        let row = lookup spacing
            where lead_min <= team.score_diff and lead_max >= team.score_diff
            else { lead_min: 0, lead_max: 0, spread: 6, press: 5 }
        kind: "hold",
        score: 20 + row.press,
        spread: row.spread,
    }
}
```

The first matching row wins, or with `order by col [desc]` the first row with
the smallest (or largest) value. `else` gives a row when none matches. A
`let` in a `then` block is visible to every parameter. Lookups do not allocate.

Note: before this release a `lookup` inside a `when` block compiled to broken
bytecode, and a `then`-block `let` reached only the first parameter. Both are
fixed; rebuild bundles that used them.

## Memory and cooldowns

`gameai.Memory` keeps, for up to 16 keys per agent, the tick a key last fired
and how many times. Rules read it as `mem.last.<key>` and `mem.count.<key>`
when the view has a `Memory` (or `*Memory`) field tagged `arb:"mem"`:

```arb
when { tick - mem.last.dash >= 90 }   # a 90-tick cooldown
```

A key that never fired reads as `gameai.Never` (-2^40), so the cooldown test
passes, and `tick - Never` stays an exact integer.

Memory is a fixed-size value with no hidden state, so it is part of the
simulation state like positions are: copying it is a snapshot, assigning the
copy back is a restore, and a rollback restores cooldowns with everything
else. `MarshalBinary` gives a deterministic encoding for network snapshots.
When a 17th key arrives, the key that fired longest ago is evicted (ties: the
smaller key), so every peer evicts the same one.

Anything else an agent remembers (a target id, a last position) belongs in
your own state and reaches the rules through the view.

## Randomness

Randomness must come from state every peer shares. Derive it, do not draw it:

```go
seed := gameai.Seed(matchSeed, uint64(tick), uint64(agent), gameai.Key("tactic"))
```

`Seed` mixes its inputs with fixed integer arithmetic (splitmix64). A seed
derived from the match seed, tick, agent and decision needs no stored state,
so there is nothing to snapshot and replaying a tick reproduces its draws.
`gameai.Unit(seed)` maps a seed to a float64 in [0, 1). To let a rule fire with
some probability, pass a roll in the view:

```arb
rule Feint tag "ability" { when { roll < 0.15 and self.has_orb } then Ability { kind: "feint" } }
```

`gameai.Rand` is a splitmix64 stream for games that want one; its whole state
is one `uint64`.

## Determinism

For the same bundle and the same inputs, `gameai` makes the same decisions on
amd64, arm64 and js/wasm. The test suite checks this with digests over 60,000
randomized evaluations (`gameai/determinism_test.go`) and over a one-minute
toy match of 118,800 decisions (`examples/gameai/arena/determinism_test.go`);
`cmd/arbiter-parity` replays the match in CI on amd64 v1 and v3, js/wasm and
native arm64. TinyGo's wasm target gives the same match digest.

What the engine guarantees:

- **Order.** Rules evaluate in declaration order; matches and action
  parameters come back in declaration order. No result depends on Go map
  iteration.
- **Numbers.** `number` is IEEE float64. `+ - * /` are correctly rounded on
  every target; the VM rounds every product explicitly so arm64 cannot fuse a
  multiply and add (`scripts/check-no-fma.sh` checks the build), `%` uses a
  pure-Go `math.Mod`, and NaN results are canonicalized. Arbiter has no
  transcendental functions, so there is no libm to differ. The opt-in integer
  profile (`vm.NumericInteger`, SPEC section 3.7) turns any non-integer or
  out-of-range arithmetic into an error, for games that want fixed-point
  discipline.
- **Decimals.** `decimal` values are exact and deterministic but allocate on
  every operation. Use `number` in per-tick rules.
- **Selection.** Ties and draws follow the rules above, with explicit float64
  rounding.
- **No clock.** Rules that read the wall clock are rejected at load.

What the game must keep deterministic: the inputs. Build views from simulation
state only, in a fixed agent order, and keep your own simulation free of
fused multiply-adds on arm64 (write `float64(a*b) + c`) or use integers, as
the arena does.

## Performance

A tick of the toy game is 22 agents x 3 decisions = 66 evaluations, each over
2 to 5 rules with table lookups, `let` bindings, memory reads and a weighted
draw. Measured with `go test -bench 'BenchmarkDecisions' ./examples/gameai/arena`:

| Target | Time per tick (66 decisions) | Per decision | Allocations |
|---|---|---|---|
| arm64, Apple M4 Pro | 28 µs | 425 ns | 0 |
| amd64, Intel Core Ultra 9 285 | 33 µs | 500 ns | 0 |
| amd64, Intel Xeon D-2141I (2.2 GHz server) | 97–110 µs | 1.5 µs | 0 |
| js/wasm in Node 26, Intel Core Ultra 9 285 | 104 µs | 1.6 µs | 0 |

`TestTickDoesNotAllocate` fails if a tick allocates. What allocates: string
concatenation, decimal arithmetic, and string fields whose value is neither a
string constant in the program nor one of the first 256 other distinct values
the binding has read. The view binding does not take slices, so quantifiers
(`any x in list`) have nothing to iterate there: precompute aggregates such as
"nearest opponent" in Go and pass them as fields, which is also faster.

## Debug traces

`Explain` is `Decide` that also fills a `gameai.Trace`:

```go
var tr gameai.Trace // keep one per watched agent
c, err := intent.Explain(&view, 0, &tr)
overlay.SetText(tr.String())
```

```text
intent best(score) -> Carry 80
- Chase: self.has_orb=true
> Carry 80: self.has_orb=true
+ Pass 77: self.has_orb=true self.pass_open=true
- Mark: team.lost_orb=false
+ Hold 26:
ability first -> Dash
> Dash: (tick - mem.last.dash)(1.099511627876e+12) >= 90 self.stamina(50) >= 30 self.orb_dist(0) < 6
- Shove: (tick - mem.last.shove)(1.099511627876e+12) >= 45 self.mark_threat(0) >= 2 NO
```

Each line is a rule: `>` the choice, `+` another candidate, `~` a candidate
from an `otherwise` action, `-` a rule whose condition did not hold. Then its
score or weight and the conditions it checked, in order: a boolean input reads
`name=value`, a comparison reads `lhs(value) op rhs` and ends in `NO` when it
was false. `and` and `or` stop early, so Chase above shows only the input that
failed `not self.has_orb`. Weighted decisions also show the draw. `tr.Candidates`, `tr.Chosen`
and `tr.VM` hold the same data for a custom overlay.

Tracing is off unless you pass a trace. Off, it costs one pointer test per
comparison; on, a reused trace does not allocate either.

## Hot reload and versions

`brain.Swap(newProg)` replaces the program atomically. It prepares every
decision first and keeps the old program if the new one lacks a decision's tag
or reads a name the view does not provide. Decision handles stay valid.

In a networked match every peer must swap at the same tick, because two
programs make different decisions:

1. The server announces "from tick N, bundle with digest D".
2. Each peer loads the bundle (`gameai.Load`) as soon as it arrives.
3. Each peer calls `Swap` between ticks N-1 and N.
4. Snapshots and replays record `brain.Program().Digest()`; a rollback past
   tick N swaps the old program back.

In development a server can recompile on file change with
`build.CompileFile` and swap `res.Program` in directly.

## Browser builds

`gameai` imports only the VM, the bundle codec and the bytecode definitions.
`gameai/deps_test.go` fails if it, `vm`, `bundle/wire` or `bytecode` ever
import the parser, the IR, the compiler or gotreesitter again. A minimal
program that loads a bundle and decides once
([`gameai/internal/wasmprobe`](../gameai/internal/wasmprobe/main.go)) is:

| Toolchain | Raw | brotli -q 11 |
|---|---|---|
| Go 1.26.10 js/wasm, `-trimpath -ldflags=-s -w` | 4,383,964 bytes | 914,991 bytes |
| TinyGo 0.41.1, `-target wasm -no-debug` | 811,253 bytes | 245,628 bytes |

Most of the Go figure is the standard library (fmt, encoding/json, regexp,
math/big, crypto/sha256). In a client that already links those, Arbiter adds
738,384 bytes raw, 144,715 bytes brotli. `TestWasmSize` caps the probe at
4.5 MiB.

## Limits

- Bundles carry plain rules, tables and constants. Strategies, flags, expert
  rules, arbiters and workers are rejected by `arbiter bundle --game`; write
  each decision as rules with a shared tag.
- The selection policy (first, best, weighted) is set in Go, in the `Spec`.
- Memory holds 16 keys per agent.
- A `Brain` is for one goroutine; give each room its own and share the
  `Program`.
