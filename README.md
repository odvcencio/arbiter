# arbiter

A compact language for governed outcomes.

Every decision your software makes — approve this transaction, show this variant, block this request, compute this tax bracket — is a governed outcome. Arbiter gives those decisions a language, compiles them to bytecode, and evaluates them in **223 nanoseconds**.

```
.arb source ──→ Parser ──→ Compiler ──→ Bytecode VM (~223ns/eval)
                              │
                              ├──→ Arishem JSON (compatibility)
                              ├──→ CEL
                              ├──→ Rego / OPA
                              └──→ Drools DRL
```

Two runtimes, one language. **Stateless evaluation** for request-path decisions. **Expert inference** for forward-chaining reasoning until quiescence. Same compiler, same VM, same governance.

The parser is built on [gotreesitter](https://github.com/odvcencio/gotreesitter), a pure-Go reimplementation of the tree-sitter runtime — no CGo, no C toolchain, no generated files. Cross-compiles to any `GOOS`/`GOARCH` target Go supports, including WASM.

## Performance

Arbiter's numbers come from this repo's benchmarks. Cross-engine runtime comparisons against CEL and OPA are in [`benchmarks/runtime`](benchmarks/runtime).

| Metric | Arishem | Arbiter | Factor |
|--------|---------|---------|--------|
| 10K rule compile memory | 7.8 GB | 72 MB | **108x less** |
| 10K rule allocations | 153M | 940K | **163x fewer** |
| 5K rule eval memory | 3.9 GB | 160 KB | **24,375x less** |
| Single rule eval | ~1.4ms | ~223ns | **~6,300x faster** |

| Engine | ns/op | B/op | allocs/op |
|--------|-------|------|-----------|
| **Arbiter** | 223 | 96 | 3 |
| CEL | 82 | 24 | 2 |
| OPA/Rego | 5,680 | 6,444 | 114 |

CEL is ~2.7x faster on a bare boolean predicate — it's a lean expression evaluator. Arbiter carries rule-engine machinery (governance gates, action resolution, constant pool) and is still in the same class. OPA is 25x slower with 67x more allocations.

Fixed 256-element stack. Zero heap allocation during evaluation. The constant pool interns all strings and numbers — 10K rules referencing the same field names share one copy.

## Governance

Segments, rollouts, kill switches, prerequisites, explainability — governance primitives that apply to any outcome. Rules, flags, and expert inferences all share them.

### Rules

```python
rule FreeShipping {
    when {
        user.cart_total >= 35
        and user.region != "XX"
    }
    then ApplyShipping {
        cost: 0,
        method: "standard",
    }
}
```

Rules support governance keywords directly:

```python
rule EnhancedRiskCheck priority 1 {
    kill_switch
    requires BasicRiskCheck
    when segment high_risk {
        tx.amount > 5000
    }
    then Flag { level: "hold" }
    rollout 20
}
```

### Segments

Reusable conditions. Define once, reference from any rule or flag.

```python
segment beta_users {
    user.cohort matches "^beta_"
}

segment high_value {
    user.lifetime_spend > 10000
}
```

### Feature Flags

Flags add one concept to the governance model: **variants** — named outcomes with typed payloads. Everything else (segments, rollouts, kill switches, prerequisites, explainability) is shared.

```python
flag checkout_v2 {
    description: "New checkout flow"
    default: control

    variant treatment {
        show_new_ui: true,
        layout: "single_page",
    }

    when segment beta_users serve treatment
    when { user.id % 100 < 20 } serve treatment rollout 50
}
```

Schema validation, secret references, request-level caching, hot reload, HTTP serving, explainability traces — all come along.

### Expert Inference

Forward-chaining rules that reason until quiescence. Facts build on facts. Rules fire, assert new facts into working memory, and the engine loops until nothing changes.

```python
expert rule ComputeAGI priority 15 {
    requires ComputeGrossIncome
    when {
        any gi in facts.GrossIncome { true }
    }
    then assert AGI {
        key: "total",
        amount: income.wages + income.interest - deductions.hsa,
    }
}

expert rule EmitDetermination priority 90 {
    requires ComputeTaxableIncome
    when { true }
    then emit Determination {
        status: "complete",
    }
}
```

Two action kinds: `assert` mutates working memory (triggers more rule firings), `emit` produces a final outcome (does not trigger re-firing). The session runs with guardrails — configurable max rounds and max mutations, context cancellation. Every firing is recorded in the activation trace.

### Explainability

Every evaluation produces a full decision trace — stateless rules, flags, and expert sessions alike.

```go
// Stateless rules
matched, trace, _ := arbiter.EvalGoverned(ruleset, dc, segments, ctx)

// Flags
eval := flags.Explain("checkout_v2", ctx)

// Expert inference
result, _ := session.Run(ctx)
result.Activations // every firing, every round, what changed
```

### Runtime Overrides

Kill switches and rollout percentages can be changed at runtime without recompiling. The override store layers on top of compiled governance fields.

```go
store.SetRule("bundle_id", "RiskyRule", overrides.RuleOverride{
    KillSwitch: ptr(true),
})

store.SetFlag("bundle_id", "new_feature", overrides.FlagOverride{
    KillSwitch: ptr(true),
})
```

## Serving

### gRPC API

Arbiter ships a gRPC server that bundles compilation, evaluation, flag resolution, expert sessions, runtime overrides, and audit logging into a single service.

```protobuf
service ArbiterService {
    rpc PublishBundle(...)       // compile and register .arb source
    rpc EvaluateRules(...)      // stateless rule evaluation
    rpc ResolveFlag(...)        // flag resolution with explainability
    rpc SetRuleOverride(...)    // runtime kill switch / rollout changes
    rpc SetFlagOverride(...)    // runtime flag kill switch
    rpc SetFlagRuleOverride(...)// runtime flag rule rollout changes
}
```

Bundles are published once and evaluated many times. Each bundle compiles rules, expert rules, flags, and segments from a single `.arb` source.

### Audit

Every governance decision is written to a durable audit sink. The default `JSONLSink` appends one JSON object per line to a file. Implement the `audit.Sink` interface for your backend (database, event stream, object store).

```go
sink, _ := audit.NewJSONLSink("/var/log/arbiter/decisions.jsonl")
server := grpcserver.NewServer(registry, overrides, sink)
```

Each audit event captures the full context: matched rules, flag resolutions, expert session outcomes, governance trace steps, timestamps, request IDs, and bundle IDs.

## Install

```bash
go install github.com/odvcencio/arbiter/cmd/arbiter@latest
```

## Usage

### CLI

```bash
arbiter compile rules.arb          # compile and show stats
arbiter eval rules.arb --data '{...}'  # evaluate against data
arbiter emit rules.arb             # emit Arishem JSON
arbiter emit rules.arb --rule Name # emit single rule
arbiter check rules.arb            # validate without emitting
```

### Go Library — Stateless Rules

```go
ruleset, _ := arbiter.Compile(source)
dc := arbiter.DataFromMap(data, ruleset)

// Fast path — no governance
matched, _ := arbiter.Eval(ruleset, dc)

// Governed path — segments, kill switches, rollouts, prerequisites, trace
result, _ := arbiter.CompileFull(source)
matched, trace, _ := arbiter.EvalGoverned(result.Ruleset, dc, result.Segments, ctx)
```

### Go Library — Flags

```go
f, _ := flags.Load(source)
variant := f.Variant("checkout_v2", ctx)
eval := f.Explain("checkout_v2", ctx)
f, _ = flags.Watch("flags.arb")          // hot reload
http.Handle("/flags", f.Handler())        // serve over HTTP
```

### Go Library — Expert Inference

```go
program, _ := expert.Compile(source)
session := expert.NewSession(program, envelope, initialFacts, expert.Options{
    MaxRounds:    32,
    MaxMutations: 1024,
})
result, _ := session.Run(ctx)

for _, outcome := range result.Outcomes {
    fmt.Printf("%s → %s %v\n", outcome.Rule, outcome.Name, outcome.Params)
}
fmt.Printf("quiesced in %d rounds, %d mutations\n", result.Rounds, result.Mutations)
```

### Migrating from Arishem

```go
// Before (Arishem — 7.8GB for 10K rules)
rule, _ := arishem.NewPriorityRule(name, priority, condJSON, actJSON)
dc, _ := arishem.DataContext(ctx, inputJSON)
arishem.ExecuteRules([]arishem.RuleTarget{rule}, dc)

// After (Arbiter — 72MB for 10K rules, ~223ns/rule eval)
ruleset, _ := arbiter.CompileJSONRules([]arbiter.JSONRule{{name, priority, condJSON, actJSON}})
dc, _ := arbiter.DataFromJSON(inputJSON, ruleset)
matched, _ := arbiter.Eval(ruleset, dc)
```

## Language

### Features

Declare the data your rules evaluate against.

```python
feature user from "user-service" {
    age: number
    tier: string
    region: string
    cart_total: number
    tags: list<string>
}
```

### Constants

Named values inlined at compile time.

```python
const VIP_THRESHOLD = 1000
const PREMIUM_TIERS = ["gold", "platinum"]
```

### Rules

```python
rule RuleName priority 1 {
    kill_switch                    # optional: instant disable
    requires OtherRule             # optional: prerequisite
    when segment high_value {      # optional: segment gate
        condition expression
    }
    then ActionName {
        key: value,
    }
    otherwise FallbackAction {     # optional: when condition is false
        key: value,
    }
    rollout 50                     # optional: percentage gate
}
```

### Expert Rules

```python
expert rule RuleName priority 1 {
    kill_switch
    requires OtherRule
    when { condition }
    then assert FactType {         # assert: mutate working memory
        key: identifier,
        field: value,
    }
    rollout 50
}

expert rule EmitResult priority 99 {
    when { condition }
    then emit OutcomeName {        # emit: produce final outcome
        field: value,
    }
}
```

### Operators

**Comparison**

```python
x == 1          x != 1
x > 1           x < 1
x >= 1          x <= 1
```

**Logical**

```python
a and b         a or b          not a
```

**Collection**

```python
role in ["admin", "mod"]
role not in ["banned"]
tags contains "vip"
tags not contains "spam"
a retains b                     # set intersection
a not retains b
a subset_of b
a superset_of b
a vague_contains b              # fuzzy substring match in list
```

**String**

```python
name starts_with "Dr"
email ends_with ".edu"
code matches "^[A-Z]{3}$"
```

**Null**

```python
value is null
value is not null
```

**Range**

```python
age between [18, 65]            # inclusive both ends
score between (0, 100)          # exclusive both ends
temp between [0, 100)           # inclusive left, exclusive right
temp between (0, 100]           # exclusive left, inclusive right
```

**Math**

```python
price * quantity > 1000
score + bonus >= threshold
```

**Quantifiers**

```python
any item in cart.items { item.price > 100 }
all item in cart.items { item.in_stock == true }
none item in cart.items { item.banned == true }
```

**Grouping**

```python
(a > 1 or b > 2) and c > 3
```

## Architecture

```
intern/       Constant pool — deduplicates strings and numbers across all rules
compiler/     CST → bytecode compiler + Arishem JSON loader
vm/           Stack-based bytecode VM (fixed 256-element stack, zero-alloc eval)
govern/       Governance primitives: segments, rollouts, kill switches, prerequisites, trace
flags/        Feature flags: variants, schema validation, secret references, hot reload
expert/       Forward-chaining inference: working memory, assert/emit, quiescence detection
audit/        Durable decision logging (Sink interface, JSONL default)
overrides/    Runtime governance overrides (kill switches, rollout percentages)
grpcserver/   gRPC service: bundle registry, evaluation, flag resolution, expert sessions
emit/         Code generators: Rego, CEL, Drools DRL
decompile/    Bytecode → Arishem JSON
```

Flat `[]byte` of fixed-width 4-byte instructions: `[opcode(1B), flags(1B), arg(2B)]`. Constant pool indices are `uint16`, giving 65K unique values per type. The parser uses [gotreesitter](https://github.com/odvcencio/gotreesitter), so any editor with tree-sitter support gets syntax highlighting, folding, and structural navigation for `.arb` files.

## Examples

### E-commerce Pricing

```python
const PREMIUM_TIERS = ["gold", "platinum"]

rule VIPDiscount priority 2 {
    when {
        user.tier in PREMIUM_TIERS
        and user.purchase_count > 10
        and user.cart_total >= 1000
    }
    then ApplyDiscount {
        type: "percentage",
        amount: 15,
        reason: "VIP loyalty discount",
    }
}
```

### Fraud Detection

```python
rule InstantBlock priority 0 {
    kill_switch
    when {
        account.flagged == true
        or model.risk_score > 0.95
    }
    then Block {
        reason: "flagged account or extreme risk",
        escalate: "fraud-ops",
    }
}

rule GeoMismatch priority 3 {
    requires InstantBlock
    when segment untrusted_region {
        tx.amount > 100
        and account.has_2fa == false
    }
    then Challenge {
        type: "sms_otp",
        timeout: "5m",
    }
    rollout 50
}
```

### Tax Computation (Expert)

```python
expert rule ComputeGrossIncome priority 5 {
    when { income.wages > 0 or income.interest > 0 }
    then assert GrossIncome {
        key: "total",
        amount: income.wages + income.interest
            + income.dividends + income.capital_gains,
    }
}

expert rule ComputeAGI priority 15 {
    requires ComputeGrossIncome
    when { any gi in facts.GrossIncome { true } }
    then assert AGI {
        key: "total",
        amount: income.wages + income.interest
            - deductions.student_loan - deductions.hsa,
    }
}

expert rule EmitDetermination priority 90 {
    requires ComputeAGI
    when { any agi in facts.AGI { agi.amount > 0 } }
    then emit TaxReturn {
        status: "complete",
    }
}
```

## License

Apache 2.0
