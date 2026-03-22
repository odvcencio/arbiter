# Strategy Primitive Design

## Problem

A bag of rules can be made to behave like a decision tree, but correctness depends on priority conventions, mutual-exclusion discipline, and reader inference. These are exactly the concerns a language primitive should eliminate.

Strategy turns implicit exclusivity, accidental overlap, and precedence bugs into explicit recognition-and-selection semantics, obvious fallback, and compiler-checkable intent.

## Position in the Primitive Hierarchy

Strategy sits inside Arbiter's stateless governed evaluation surface. Conceptually it lives between ordinary rules and flags: rules can produce many applicable outcomes, strategy recognizes one named decision shape and chooses one ordered path, and flags add variant resolution on top.

| Primitive | Purpose |
|-----------|---------|
| rules | Independent applicable outcomes |
| **strategy** | **Recognized decision shape with exactly one governed path selected** |
| flags | Governed variant/config resolution |
| expert | Iterative inference over facts |
| arbiter | Long-running loop around the above |

## Design Principles

1. **Lowering feature, not a VM feature.** Strategy is a new declaration lowering + a first-class IR node. It executes by compiling each candidate into existing governed rule machinery, not by growing the VM instruction set.
2. **Recognition and one-of selection are explicit.** Ordered candidates, exactly one recognized winner, required fallback, full trace of why each candidate matched or was skipped.
3. **Reuses the schema system.** A strategy returns a typed `outcome`. All candidate param blocks are validated against it at compile time.
4. **Native constructs do not wait for exporters.** Strategy is an Arbiter-native construct. No emit to CEL, Drools, or Rego, and no compatibility exporter gets veto power over the primitive surface.

## Product Fit vs Emitters

The key product question is not "can we emit this elsewhere?" It is "does this make Arbiter materially better at governed decisions?"

For strategy, the answer is yes:

- It closes a real semantic gap in Arbiter's native language.
- It removes priority folklore and overlap bugs from a common class of decision logic.
- It gives `explore`, testing, and trace surfaces a first-class recognized-path model instead of making them reconstruct intent from a bag of rules.

Working product definition:

> A strategy is a named decision shape recognized from current facts/state, with a governed response attached.

For the cross-language emitter package, the answer is weaker:

- It can reassure evaluators who want a compatibility story.
- It can help a narrow migration workflow.
- But it does not strengthen Arbiter's core thesis, and it should not tax the design of native primitives.

Conclusion: **strategy should ship native-first, while emitters are compatibility adapters and can be cut or frozen without blocking language evolution.**

## Syntax

```arb
outcome CheckoutPath {
    target: string
    reason: string
}

strategy CheckoutRouting returns CheckoutPath {

    when {
        migration.shim_enabled == true
    } rollout 20 then NewStack {
        target: "new"
        reason: "gradual migration"
    }

    when {
        let needs_review = risk.requires_review == true
        needs_review
    } then ManualPath {
        target: "manual"
        reason: "review required"
    }

    else LegacyPath {
        target: "legacy"
        reason: "default fallback"
    }
}
```

### Grammar Rules

- `strategy_declaration` → `strategy` NAME `returns` OUTCOME_TYPE `{` candidate+ `}`
- Each candidate is either:
  - A `when` arm: `when_block` + optional governance + `then` LABEL `{` params `}`
  - An `else` arm: LABEL `{` params `}`
- The `else` arm is required and must be last.
- Candidate labels (NewStack, ManualPath, LegacyPath) are local to the strategy — trace/tooling identifiers, not references to external declarations.
- Governance on candidates: `rollout`, `segment`, `kill_switch` — same syntax as rule governance, reused unchanged.
- `let` bindings inside `when { ... }` are allowed and are in scope for the candidate payload block, matching ordinary rule behavior.

### What Is Intentionally Absent

- **No `priority`.** Order is syntactic (top to bottom). Explicit ordering is the point.
- **No `requires`/`excludes`.** Mutual exclusion is structural, not declared.
- **No `per_fact` or temporal modifiers.** Strategy is synchronous one-shot evaluation. Expert rules can call strategies, but strategies don't do inference.

## IR Node

Added to `ir.Program` alongside existing declaration slices:

```go
type Strategy struct {
    Name       string
    Returns    string
    Span       Span
    Candidates []StrategyCandidate
}

type StrategyCandidate struct {
    Label        string
    Segment      string
    Lets         []LetBinding
    Condition    ExprID
    HasCondition bool
    KillSwitch bool
    Rollout    *Rollout
    Params     []ActionParam
    IsElse     bool
    Span       Span
}
```

Key choices:

- `StrategyCandidate.Condition` uses the same `ExprID` into the shared expression pool. No new expression machinery.
- `HasCondition` and `IsElse` are explicit booleans. No sentinel `ExprID` tricks.
- `Rollout` is the same struct rules already use.
- `ActionParam` and `LetBinding` are the same structs rules already use.
- `Program` gets `Strategies []Strategy` and `strategyIndex map[string]int` for O(1) lookup.

## Lowering (CST → IR)

New function in `ir/lower.go`:

```go
func (l *lowerer) lowerStrategy(n *gotreesitter.Node) (Strategy, error)
```

The lowerer walks CST children of `strategy_declaration`:

1. Extract `name` from identifier field.
2. Extract `returns` type name.
3. For each `strategy_candidate`:
   - Extract the label.
   - If `when` clause: lower `let` bindings and condition expression via existing `lowerExpr`.
   - If governance clauses: extract rollout/segment/kill_switch using existing helpers.
   - Lower each param via the same param-assignment lowering used by rules and flags.
4. For the `else` child: mark `IsElse = true`, lower params, and reject governance structurally in the grammar.
5. Append to `l.program.Strategies`.

No new parsing subsystem. Condition lowering, action-param lowering, rollout extraction, and schema validation are all reused as-is.

## Compilation (IR → Bytecode)

Strategy should not extend the main VM contract. Instead, the `strategy/` package compiles each strategy into a **synthetic ruleset** whose ordered rules correspond one-to-one with strategy candidates.

```go
func compileStrategyRuleset(program *ir.Program, s *ir.Strategy) (*compiler.CompiledRuleset, error)
```

Each candidate lowers to a synthetic `ir.Rule`:

```go
ir.Rule{
    Name:       candidate.Label,
    Segment:    candidate.Segment,
    Lets:       candidate.Lets,
    Condition:  candidate.Condition,
    HasCondition: candidate.HasCondition,
    KillSwitch: candidate.KillSwitch,
    Rollout:    candidate.Rollout,
    Action: ir.Action{
        Name:   candidate.Label,
        Params: candidate.Params,
    },
}
```

The `else` arm becomes a synthetic rule with a literal `true` condition. That keeps evaluation inside the existing rule VM path and ensures candidate-local `let` bindings work for payload expressions exactly the same way they already do for rules.

This is the important simplification:

- No new opcode.
- No `CompiledRuleset.Strategies` field.
- No second evaluation engine.
- No strategy-specific bytecode format.

## Evaluation

New `strategy/` package parallel to `flags/` and `expert/`:

```go
func (s *Strategies) Evaluate(name string, ctx map[string]any) (Result, error)
```

### Algorithm

Linear scan of candidates, top to bottom:

```
for each candidate in s.Candidates:
    1. if kill_switch → skip, record in trace
    2. if segment → evaluate against ctx; false → skip, record in trace
    3. if condition → evaluate the synthetic rule condition; false → skip, record in trace
    4. if rollout → govern.DecidePercentRollout(...); miss → skip, record in trace
    5. MATCH — build the synthetic rule action payload, stop
```

If all `when` arms are exhausted, the `else` arm matches unconditionally.

### Result

```go
type Result struct {
    Strategy string            // "CheckoutRouting"
    Outcome  string            // "CheckoutPath"
    Selected string            // "NewStack"
    Params   map[string]any    // {target: "new", reason: "gradual migration"}
    Trace    govern.Trace      // full evaluation trace
}
```

### Properties

- **Exactly one winner, always.** The required `else` arm guarantees this.
- **Short-circuit.** First candidate that passes all gates wins. Remaining candidates are not evaluated.
- **Recognition is closed-world and deterministic.** A strategy does not do similarity search or scoring. It recognizes one of the explicitly declared candidate shapes.
- **Per-candidate governance with fallthrough.** A candidate can match its condition but be skipped by its rollout, falling through to the next candidate.
- **No new VM mode.** Strategy orchestration is new; bytecode execution is not.

## Traces

Strategy reuses `govern.Trace` and `govern.TraceStep` directly.

### Naming Convention

`strategy:StrategyName/CandidateLabel:gate` where gate is one of: `condition`, `rollout`, `segment`, `kill_switch`, `fallback`.

### Example Trace

```
strategy:CheckoutRouting/NewStack:condition    → true   "migration.shim_enabled == true"
strategy:CheckoutRouting/NewStack:rollout      → false  "bucket 7823 > threshold 2000 (20%)"
strategy:CheckoutRouting/ManualPath:condition  → false  "risk.requires_review == false"
strategy:CheckoutRouting/LegacyPath:fallback   → true   "else arm selected"
```

This gives:
- **`arbiter strategy`:** Direct evaluation surface for one named strategy with a structured result and trace.
- **`arbiter explore`:** Declaration-level summary of return type, candidates, and governance.
- **Generated UI:** Waterfall visualization is a natural next step because the trace is already candidate-scoped.

## Tooling Integration

### `arbiter explore`

Strategy shows up as a top-level declaration. Exploring it displays candidates in order, return type, and governance per arm.

### `arbiter strategy`

Provide input context, execute one named strategy, and inspect `result.Selected`, `result.Params`, and trace entries. The `else` guarantee means every evaluation produces a result.

### LSP

Hover on strategy name shows return type and candidate count. Hover on candidate label shows condition and governance summary. Go-to-definition on `returns` jumps to the outcome schema. Completion inside param blocks offers fields from the return type.

### Per-Bundle Generated UI

Decision flowchart: ordered candidate boxes with condition/governance annotations, winner arrow to outcome, return type schema alongside.

## Static Checks

### Structural (Compile Errors)

- `else` arm is required and must be last.
- At least one `when` arm before the `else`.
- Candidate labels must be unique within the strategy.
- No governance clauses on the `else` arm.

### Schema (Compile Errors)

- `returns` must reference a declared `outcome` schema.
- Every candidate's param block must satisfy the return type: required fields present, no unknown fields, types match.

### Reachability (Warnings)

- If a `when` candidate has no condition and no governance, everything after it is unreachable.
- If two adjacent candidates have identical conditions, the second is shadowed.
- Best-effort — full condition subsumption analysis is out of scope.

### Cross-Declaration (Compile Errors)

- Segments referenced in candidate arms must exist.

No new validation infrastructure. Every check is a structural assertion on the IR node or a call to existing validation.
