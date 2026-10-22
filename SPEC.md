# Arbiter Language Specification

Version: 0.8.0
Status: Canonical reference for frozen semantics and provisional surface area.

---

## FROZEN (stable contract -- will not change)

Everything in this section is a binding guarantee. Conformant implementations
must produce identical results for identical inputs.

---

### 1. Declarations

#### 1.1 `rule`

```
rule <Name> [priority <N>] {
    [kill_switch]
    [active_from <timestamp>]
    [active_until <timestamp>]
    [requires <RuleName>]*
    [excludes <RuleName>]*
    [rollout [percent] <N> [by <subject>] [namespace <string>]]
    when [segment <SegmentName>] { <expr> }
    then <ActionName> { [<key>: <expr>]* }
    [otherwise <ActionName> { [<key>: <expr>]* }]
}
```

- **priority**: integer metadata carried on match results. Does NOT affect
  evaluation order; rules always evaluate top-to-bottom.
- **kill_switch**: bypasses the rule during governed evaluation.
- **active_from / active_until**: structural stateless eligibility window.
  `active_from` is inclusive; `active_until` is exclusive. If `__now` is not
  present in the request context, evaluation uses current UTC time.
- **requires**: named rule must have matched or named flag must resolve
  non-default before this rule's condition is evaluated.
- **excludes**: if the named rule was evaluated and matched, this rule is
  skipped. If not yet evaluated, this rule is deferred (treated as not matched,
  but recorded as `disposition=deferred` in the arbitrace).
- **when**: condition block. May combine a named segment and an inline
  expression; both must be true.
- **then/otherwise**: action produced on match/non-match. `otherwise` only
  fires during governed evaluation.
- **rollout**: percent-based progressive delivery (see 4.2).

#### 1.2 `expert rule`

```
expert rule <Name> [priority <N>] [per_fact] [cooldown <dur>] [debounce <dur>] {
    [kill_switch] [no_loop] [stable]
    [requires <RuleName>]* [excludes <RuleName>]*
    [activation_group <GroupName>]
    when [segment <Seg>] {
        [let <name> = <expr>]*
        <expr> | <binding-clause>
    } [for <dur>] [within <dur>] [stable_for <N> cycles]
    then <assert|emit|retract|modify> <Schema> {
        [<key>: <expr>]*
        [set { [<key>: <expr>]* }]   # modify only
    }
    [rollout [percent] <N> [by <subject>] [namespace <string>]]
}
```

- **per_fact**: fires independently per matching fact instance.
- **no_loop**: suppresses re-fire when the rule's own output is the sole
  cause of re-evaluation.
- **stable**: defers activation until a quiescent round (zero mutations).
- **activation_group**: only one rule per group fires per round.
- **Temporal constraints** (exact semantics in 2.5):
  `for <D>` -- condition must hold continuously for D.
  `within <D>` -- must fire within D of triggering fact's assertion.
  `stable_for <N> cycles` -- must hold across N consecutive fixpoint rounds.
  `cooldown <D>` -- suppressed for D after firing.
  `debounce <D>` -- waits D after condition becomes true; resets if interrupted.
- **Action kinds**: `assert` (upsert fact), `emit` (produce outcome),
  `retract` (remove fact), `modify` (update fields via `set` block).

#### 1.3 `strategy`

```
strategy <Name> returns <OutcomeSchema> {
    when [segment <Seg>] { <expr> }
        [kill_switch] [active_from <timestamp>] [active_until <timestamp>]
        [rollout ...] then <Label> { [<key>: <expr>]* }
    [when ... then <Label> { ... }]*
    [else <Label> { [<key>: <expr>]* }]
}
```

Ordered candidates; first match wins. `else` is unconditional fallback.
Each candidate may have its own `kill_switch`, active window, and `rollout`.
The `returns` clause names the outcome schema populated by the selected
candidate.

#### 1.4 `flag`

```
flag <Name> [type boolean|multivariate] [default <value>] [kill_switch] {
    [<metaKey>: <string>]*
    [requires <FlagName>]*
    [variant <string> { [<key>: <expr>]* }]*
    [defaults { [<key>: <expr>]* }]
    [[active_from <timestamp>] [active_until <timestamp>]
     when <segment|{expr}|segment{expr}> [rollout ...] then <variant>
        | split [by <subject>] [namespace <string>] { [<variant>: <weight>]* }
    ]*
}
```

- `boolean` or `multivariate` type. Default variant returned on no-match,
  kill-switch, or prerequisite failure.
- Flag rules evaluate top-to-bottom; first match wins.
- `split`: weighted deterministic assignment via rollout bucketing.
- `defaults`: payload merged under every variant.

#### 1.5 `fact`

```
fact <Name> { [<field> [?]: <schema_type>]* }
```

Expert-system working-memory schema. Implicit `key` field (string, required)
not declared in schema. Facts upserted by type+key.

#### 1.6 `outcome`

```
outcome <Name> { [<field> [?]: <schema_type>]* }
```

Typed outcome shape. No implicit `key` field. Used by `emit`, strategy
`returns`, and worker I/O.

#### 1.7 `feature`

```
feature <Name> from <source_string> { [<field>: <type>]* }
```

External data shape. Field types: `string`, `number`, `bool`, `list<T>`.

#### 1.8 `worker`

```
worker <Name> {
    input <OutcomeSchema>
    output <FactSchema|OutcomeSchema>
    <runtime_kind> [<target>]
}
```

Input must reference an outcome schema. Output may reference a fact or outcome
schema. Runtime kinds: `exec`, `webhook`, `grpc`, `slack`, `audit`, `stdout`.
`stdout` takes no target; all others require one.

#### 1.9 `arbiter`

```
arbiter <Name> {
    <trigger>+
    [source <target>]*
    [checkpoint <target>]
    [on <Outcome|*> [where <expr>] <handler_kind> [<target>]]*
}
```

Triggers (at least one required): `poll <duration>`, `stream <target>`,
`schedule <cron> [source <target>]`.

Handlers: `webhook`, `slack`, `chain`, `exec`, `worker`, `grpc`, `audit`,
`stdout`. `chain` propagates outcomes as facts to another arbiter. `worker`
invokes a named worker (outcome must match worker input). `*` matches all
outcomes but is not allowed with `worker`.

#### 1.10 `segment`

```
segment <Name> { <expr> }
```

Named compiled condition, cached per request in RequestCache.

#### 1.11 `const`

```
const <Name> = <expr>
```

Compile-time constant, inlined at every reference site.

#### 1.12 `include`

```
include "<path>"
```

Textual inclusion. Path resolved relative to the including file's directory.

---

### 2. Evaluation Semantics

#### 2.1 Rule Evaluation

1. Rules evaluate in **top-to-bottom declaration order**.
2. **All** matching rules are returned; no short-circuit.
3. `priority` is metadata on match results, not eval order.
4. On match: `then` action built. On non-match with `otherwise`: fallback built.

#### 2.2 Governed Evaluation

For each rule, in declaration order:

1. **Kill switch** -- if enabled, skip. Record not-matched.
2. **Prerequisites** -- check RequestCache. If any prerequisite failed, skip.
3. **Excludes** -- if excluded rule not yet evaluated, skip (deferred). If
   evaluated and matched, skip.
4. **Active window** -- if `active_from` / `active_until` rejects the current
   eval time, skip.
5. **Segment** -- evaluate named segment (cached). If false, skip.
6. **Condition** -- evaluate inline expression. If false, skip (build fallback
   if `otherwise` exists).
7. **Rollout** -- compute rollout decision. If not allowed, skip.
8. **Match** -- build `then` action, record as matched.

#### 2.3 Strategy Selection

1. Candidates evaluated in **declaration order**.
2. Per candidate: check kill switch, evaluate active window, evaluate segment, evaluate condition
   (`else` uses constant `true`), check rollout.
3. **First match wins.** No candidate match returns an error.

#### 2.4 Flag Resolution

1. Return cached variant if present.
2. **Cycle detection**: flag already on eval stack returns default.
3. **Kill switch**: if enabled, return default.
4. **Prerequisites**: recursively evaluate required flags. If any resolves
   default, return this flag's default.
5. **Rules**: first matching rule wins. Check active window, then rollout; if blocked, continue.
   If rule has `split`, assign via weighted bucketing. Otherwise return `then`
   variant.
6. No match returns the flag's **default** variant.

#### 2.5 Expert Inference

Forward-chaining fixpoint engine.

**Execution loop**: max 32 rounds (configurable). Each round evaluates eligible
rules against working memory. Matched rules applied in priority order. Loop
terminates on quiescence (zero mutations) or guardrail (max rounds/mutations).

**Dirty tracking**: asserted/retracted/modified facts enter the dirty set.
Only rules with intersecting fact dependencies re-evaluate in subsequent rounds.

**Temporal constraints**:
- `for <D>`: tracked via first-true timestamp. Fires when `now - first_true >= D`.
- `within <D>`: window starts at triggering fact's assertion time. Expires
  without firing if window elapses.
- `stable_for <N> cycles`: must hold across N consecutive zero-mutation rounds.
- `cooldown <D>`: suppressed for D after last fire.
- `debounce <D>`: waits D after condition becomes true. Resets if condition
  becomes false during wait.

**Activation groups**: one rule per group per round. First match claims the group.

**`per_fact`**: fires per matching fact instance, not once per round.

**`no_loop`**: prevents re-fire when own output is sole re-evaluation cause.

**`stable`**: defers until a quiescent round, then fires on the next round.

**Action semantics**:
- `assert`: upsert fact by type+key. Marks dirty.
- `emit`: append-only outcome. Does not affect working memory.
- `retract`: remove fact by type+key. Marks dirty.
- `modify`: update fields via `set` block. Fact must exist. Marks dirty.

#### 2.6 Continuous Arbiters

- **Topological execution**: chained arbiters form a DAG, executed in order.
- **Chain propagation**: outcome routed to `chain` handler is mapped to a fact
  and asserted in the target arbiter's working memory.
- **Source sync**: before each tick, external sources sync. New facts asserted,
  missing facts retracted.
- **Handler routing**: after each tick, outcomes dispatched to matching handlers.
  `where` filters evaluated per outcome. `*` matches all.

---

### 3. Type System

#### 3.1 Schema Types

`string` -- UTF-8. `number` -- IEEE 754 float64 (see 3.7 for the integer
numeric profile). `decimal` -- exact
fixed-point. `bool`/`boolean`. `timestamp` -- RFC 3339.
`number<dimension>` -- float64 with dimension. `decimal<currency>` -- exact
decimal with currency.

#### 3.2 Fact and Outcome Schemas

Facts have an implicit `key: string` (required, not declared). Outcomes have
no implicit key. Optional fields use `?` suffix on the field name.

#### 3.3 Decimal Arithmetic

Exact fixed-point via `math/big`, 10-digit precision.

- **add/sub**: same-unit required; result inherits unit.
- **mul**: one unitless operand OK; result inherits the other's unit. Two
  units: compile error.
- **div**: unitless divisor preserves dividend unit. Same-unit division yields
  unitless.
- **mod**: same rules as div.

#### 3.4 Quantity Literals

Syntax: `<number> <unit>` (e.g., `100 USD`, `30 km`). Normalized to base
units at compile time. Same-dimension comparison uses base values.
Cross-dimension comparison is a compile-time error when schemas are available.

#### 3.5 Unit Dimensions

19 dimensions, 85 unit symbols:

| Dimension | Symbols |
|-----------|---------|
| temperature | K, C, F |
| length | mm, cm, m, km, in, ft, yd, mi |
| mass | mg, g, kg, lb, oz |
| time | ms, s, min, hr, d |
| volume | mL, L, gal, fl_oz |
| pressure | Pa, hPa, kPa, bar, psi, atm |
| percentage | pct, % |
| concentration | ppm, ppb |
| area | mm2, cm2, m2, km2, ha, acre |
| speed | m/s, km/h, mph, kn |
| electric_current | mA, A |
| voltage | mV, V, kV |
| power | W, kW, MW, hp |
| energy | J, kJ, kWh, cal, kcal |
| frequency | Hz, kHz, MHz, GHz |
| data | B, KB, MB, GB, TB |
| flow | L/min, L/hr, gal/min, m3/s |
| currency | USD, EUR, GBP, JPY, CNY, CHF, CAD, AUD |
| cryptocurrency | BTC, ETH, SOL, USDC, USDT |

Currency units have `ToBase: 1` -- no cross-currency conversion. Amounts must
match exactly in arithmetic.

#### 3.6 Type Checking

- **Compile-time**: schema-bound paths type-checked at compilation.
- **Runtime**: unbound paths use coercion. Numeric strings coerce to numbers.
  Type mismatches produce errors, not panics.

#### 3.7 Deterministic Evaluation and the Integer Numeric Profile

**Cross-target determinism.** Rule evaluation gives bit-identical results on
amd64, arm64 and js/wasm. The implementation guarantees this as follows:

- Every product and quotient the VM computes (`*`, `/`, `avg`, and constant
  folding) is wrapped in an explicit `float64(...)` conversion. The Go spec says
  this rounds the value and prevents the compiler from fusing it into a later
  add (an FMA instruction on arm64, never on wasm).
- `%` on floats calls `math.Mod`. In Go 1.26 `math/mod.go` is pure Go and
  `math/stubs.go` sets `haveArchMod = false` on every GOARCH, so no target has
  an assembly path.
- The expert engine never lets Go map order reach a result. Fact lists given to
  `any`/`all`/`none` quantifiers and aggregates are sorted by fact key, so
  float sums add in one fixed order and an `any` quantifier binds the same
  item. Per-fact rules, sync, retraction and support clean-up also run in sorted
  order.

`scripts/parity.sh` (`make parity`) checks this: it runs a fixed corpus on
amd64 `GOAMD64=v1` and `v3`, js/wasm and native arm64, and fails unless all
digests match.

**Integer numeric profile.** The default profile is IEEE float64 (section 3.1).
A host that wants an arithmetic guarantee, not a convention, opts in to the
integer profile. It has a static check and a runtime guard.

*Static check.* `compiler.CheckIntegerProfile(rs)` and
`arbiter check <file.arb> --numeric-profile integer` (which also checks
segments and expert rules; `expert.Program.NumericProfileViolations` does the
same for a loaded program) work on the compiled bytecode and report these
violations:

1. An `OpLoadNum` constant, or a number inside a list literal, that is not an
   exact integer with |v| <= 2^53.
2. Any `OpLoadDec` constant or decimal inside a list literal. Decimal and
   quantity literals are therefore rejected.
3. An `OpDiv`, or an `OpAggEnd` that computes `avg`, whose next instruction is
   not `OpFloor`, `OpCeil` or `OpRound`.

Rule 3 is exact for postfix bytecode. The quotient is on top of the value stack
right after `OpDiv`. The next instruction either consumes it or pushes another
operand above it, so requiring a rounding opcode there means no other operation
can use the quotient first. `floor(a / b)` passes. `floor(a / b + 1)`,
`a / b > 2` and `let q = a / b` fail. The compiler folds a division of two
literals into one literal. A fractional constant quotient such as `floor(7 / 2)`
therefore fails rule 1; write the dividend or divisor as a field instead.

*Runtime guard.* `vm.NumericInteger`, set with `SetNumericProfile` on
`vm.NewPreparedEvaluator`'s result or on `vm.NewEvaluator`'s result, or with
`expert.Options.NumericProfile`, makes each math opcode check its operands and
result:

- `+ - * % abs min max` and each `sum` item need operands that are exact
  integers with |v| <= 2^53, and the result must also be in that range. Sums
  and products are computed in exact integer arithmetic, so a product of
  2^53 + 1 is an error and is never rounded down to 2^53.
- `/` needs integer operands. A zero divisor is an error. The quotient may be
  fractional (IEEE division is correctly rounded on every target), and only
  `floor`, `ceil` and `round` accept a fractional operand. For |a|, |b| <= 2^53,
  `floor(a / b)` equals integer floor division.
- `%` with a zero divisor is an error. The result takes the sign of the
  dividend.
- A null, string, boolean or decimal operand is an error.
- Errors carry the text `integer profile:` and abort the evaluation like any
  other runtime error. No NaN or Inf result is produced.

The runtime guard does not inspect comparisons or the values a rule only
passes through, so a host that needs the whole guarantee also requires an empty
static check result.

---

### 4. Governance

#### 4.1 Kill Switches

Binary. `kill_switch` keyword in declaration. When enabled (compiled or
overridden), the rule/flag/candidate is immediately bypassed. No condition
evaluation. Granularity: per-rule, per-flag, per-strategy-candidate.

#### 4.2 Rollouts

- Hash: `SHA256(namespace + "\x00" + subject_value)`.
- Bucket: first 4 bytes as big-endian uint32, mod 10000.
- Resolution: basis points (0--9999). `rollout 50` = 50% = 5000 bps.
- **Deterministic and sticky**: same namespace + subject = same bucket.
- **Subject resolution**: dot-path against request context. Default: `user.id`,
  fallback: `user_id` flat key.

#### 4.3 Auto-Namespace Derivation

When no explicit `namespace` is declared:

```
<bundleID>:<scope>     if bundleID non-empty
arbiter:<scope>        otherwise
```

Scope formats: `rule:<Name>`, `flag:<Name>:rule:<idx>`,
`flag:<Name>:rule:<idx>:split`, `strategy:<Name>:candidate:<Label>`.

#### 4.4 Segments

Named compiled conditions against nested request context. Cached per request --
evaluated at most once regardless of how many rules reference it.

#### 4.5 Prerequisites

`requires <name>`: rule must have matched or flag must resolve non-default.
Flags recursively evaluated on demand. **Cycle detection**: re-entering a flag
on the eval stack returns its default.

#### 4.6 Excludes

`excludes <name>`: deferred evaluation via RequestCache. Not-yet-evaluated
excluded rule causes skip (conservative deferral) and records an arbitrace step
with `result=false`, `kind=excludes`, and `disposition=deferred`.
Evaluated-and-matched causes skip. Evaluated-and-not-matched passes.

#### 4.7 Override Precedence

```
runtime overrides > compiled defaults
```

Overridable: kill_switch (bool), rollout (uint16 bps). Scopes: per-rule,
per-flag, per-flag-rule, per-strategy-candidate.

---

### 5. Arbitrace Guarantees

#### 5.1 Governed Arbitraces

Every governed evaluation produces an `Arbitrace` with ordered `ArbitraceStep` entries:

- **Check**: string -- human-readable description.
- **Result**: bool -- legacy compatibility bool; deferred steps keep `false`.
- **Disposition**: string -- canonical tri-state outcome: `passed`, `blocked`, or `deferred`.
- **Detail**: string -- explanation (values, cache state).
- **Phase / Scope / Subject / Kind / Target**: structured semantics shared across governed surfaces.

Steps appended in eval order. Typical per-rule sequence: kill_switch,
active_from/active_until, requires, excludes, segment, condition, rollout.

#### 5.2 Expert Activations

Each firing produces an `Activation` record: Round (int), Rule (string),
Kind (assert/emit/retract/modify), Target (string), Params (map), Changed
(bool), Detail (string).

Expert session results also expose:

- **stable_deferred**: whether a `stable` rule is still waiting for a quiescent round.
- **temporal_pending**: whether a temporal constraint such as `stable_for`, `for`,
  or `debounce` is still maturing.

#### 5.3 Determinism

Traces are deterministic given same .arb source, same context, same eval time.

---

### 6. Test Framework (.test.arb)

#### 6.1 Test Blocks

```
test "<name>" {
    given { <path>: <value> ... }
    expect rule <RuleName> [matched|not_matched]
    expect action <ActionName> { [<key>: <value>]* }
    expect flag <FlagName> <variant>
    expect strategy <StrategyName> selected <Label> { [<key>: <value>]* }
}
```

#### 6.2 Scenario Blocks

```
scenario "<name>" {
    given { <path>: <value> ... }
    assert <FactType> { key: "<key>", [<field>: <value>]* }
    at T+<duration> { [assert ...] [expect fact ...] [expect outcome ...] }
    after stabilization { [expect fact ...] [expect outcome ...] }
}
```

- `assert`: seeds working memory.
- `at T+<duration>`: advances simulated time.
- `after stabilization`: runs to fixpoint then asserts.

#### 6.3 Continuous Scenarios

```
scenario "<name>" {
    stream <FactType> { key: "<key>", [<field>: <value>]* }
    within <duration> { expect outcome <OutcomeType> { ... } }
}
```

#### 6.4 Field Assertions

`field: value` (exact), `field: > N`, `field: >= N`, `field: < N`,
`field: <= N`, `field: [N, M]` (between inclusive).

#### 6.5 Negation

`expect not outcome <Type> { ... }` -- asserts outcome was NOT produced.

---

### 7. Bytecode Format

#### 7.1 Instruction Encoding

Every instruction: **4 bytes** `[opcode:1B][flags:1B][arg:2B LE]`.

#### 7.2 Opcodes

54 opcodes:

| Category | Opcodes |
|----------|---------|
| Stack (6) | LoadStr, LoadNum, LoadDec, LoadBool, LoadNull, LoadVar |
| Comparison (6) | Eq, Neq, Gt, Gte, Lt, Lte |
| Collection (9) | In, NotIn, Contains, NotContains, Retains, NotRetains, VagueContains, SubsetOf, SupersetOf |
| String (3) | StartsWith, EndsWith, Matches |
| Range (4) | BetweenCC, BetweenOO, BetweenCO, BetweenOC |
| Null (2) | IsNull, IsNotNull |
| Math (11) | Add, Sub, Mul, Div, Mod, Abs, Min, Max, Round, Floor, Ceil |
| Logic (3) | And, Or, Not |
| Control (2) | JumpIfFalse, JumpIfTrue |
| Quantifier (3) | IterBegin, IterNext, IterEnd |
| Rule (1) | RuleMatch |
| Aggregate (3) | AggBegin, AggAccum, AggEnd |
| Local (1) | SetLocal |

Iterator flags: any(0), all(1), none(2). Aggregate flags: sum(0), count(1), avg(2).

#### 7.3 Constant Pool

Deduplicated by value: strings (length-prefixed UTF-8), numbers (f64),
decimals (text+unit pairs), lists (typed elements).

#### 7.4 Bundle Format

```
[ARB1]              4B magic
[string pool]       u32 count + length-prefixed strings
[number pool]       u32 count + f64 values
[decimal pool]      u32 count + (text, unit) pairs
[list pool]         u32 count + typed elements
[instructions]      u32 byte-length + raw bytes
[rule headers]      u32 count + per-rule metadata
[action entries]    u32 count + per-action params
[prereqs]           u16 string-pool indices
[excludes]          u16 string-pool indices
[content hash]      SHA-256 of all preceding bytes
```

**Obfuscation** (at marshal time): HashRuleNames, HashSegmentNames,
StripRolloutDetails, StripPrereqs. Action names and param keys are never
obfuscated.

---

### 8. Conformance Matrix

Same `.arb` + same context must produce identical results across:

| Surface | Status |
|---------|--------|
| Native Go eval | Reference implementation |
| Governed eval | Tested |
| Strategy eval | Tested |
| Binary bundle round-trip | Tested |
| Obfuscated bundle round-trip | Tested (known: string pool aliasing skips priority-ordering test) |
| JSON context round-trip | Tested |

---

## PROVISIONAL (may change -- use with awareness)

### Continuous Arbiter Runtime Surface

Poll tick loop, `stream` triggers, and `chain` propagation are stable.
Provisional: `schedule` cron triggers, checkpoint/resume, source sync protocol
beyond filesystem/chain, error recovery on handler failure.

### Worker Transport Coverage

- **Stable**: `exec`, `webhook`.
- **Log-only**: `grpc`, `slack` (implemented in agent, no e2e tests).
- **Local sinks**: `audit`, `stdout` (stable).

### LSP Feature Completeness

Stable: diagnostics, completions, hover, go-to-definition.
New/provisional: references, rename.

### SDK Ergonomics

Generated gRPC/REST stubs stable. Higher-level typed wrappers may evolve.

### Package / Module / Registry

No package or module system. Registry for sharing bundles unspecified.

### Include Resolution

Filesystem-relative: stable. HTTP, registry, remote sources: not implemented.

### Bundle Signing and Provenance

Content hash (SHA-256) for integrity. Cryptographic signing and provenance
metadata not yet specified.

### Formatter Output

Canonical formatting rules exist but are not frozen.

### Syntax Under Evaluation

- **`join` expression**: multi-binding join with `on` predicate. Complex, rarely
  used; may simplify.
- **`vague_contains` operator**: fuzzy containment. May be renamed or removed.
- **`secret()` reference**: `secret("<path>")` for runtime secrets. Provider
  interface not frozen.
- **Worker runtime options**: `grpc`, `slack`, `audit`, `stdout` may expand or
  change.
