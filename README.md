# arbiter

A compact language for governed outcomes.

Every decision your software makes — approve this transaction, show this variant, block this request, apply this discount — is a governed outcome. Arbiter gives those decisions a language, compiles them to bytecode, and evaluates them in **204 nanoseconds**.

```
.arb source ──→ Parser ──→ Compiler ──→ Bytecode VM (~204ns/eval)
                              │
                              ├──→ Arishem JSON (compatibility)
                              ├──→ CEL
                              ├──→ Rego / OPA
                              └──→ Drools DRL
```

The toolchain: a [tree-sitter grammar](https://github.com/odvcencio/gotreesitter), a bytecode compiler with constant interning, a stack-based VM, and code generators for five targets. Pure Go. No CGo. No runtime dependencies. Write governance once, run it anywhere.

## Performance

Arbiter's numbers come from this repo's benchmarks. Arishem figures are from [ByteDance/Arishem](https://github.com/bytedance/arishem) issue [#28](https://github.com/bytedance/arishem/issues/28).

| Metric | Arishem | Arbiter | Improvement |
|--------|---------|---------|-------------|
| 10K rule compile memory | 7.8 GB | 72 MB | **108x less** |
| 10K rule allocations | 153M | 940K | **163x fewer** |
| 5K rule eval memory | 3.9 GB | 160 KB | **24,375x less** |
| Single rule eval | ~1.4ms | ~204ns | **~6,900x faster** |

Fixed 256-element stack. Zero heap allocation during evaluation. The constant pool interns all strings and numbers — 10K rules referencing the same field names share one copy. The entire instruction buffer for 10K rules fits in ~800KB.

## Governance

Segments, rollouts, kill switches, prerequisites, explainability — these are governance primitives. They apply to any outcome, whether it's a rule deciding an action or a flag serving a variant.

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

### Segments

Reusable conditions. Define once, reference anywhere.

```python
segment beta_users {
    when { user.cohort matches "^beta_" }
}

segment high_value {
    when { user.lifetime_spend > 10000 }
}
```

### Kill Switches

Instant global off-switch on any governed outcome.

```python
flag payments {
    default: enabled
    variant disabled { reason: "maintenance" }

    kill_switch
    when { region in ["EU"] } serve disabled
}
```

### Rollouts

Gradual exposure. Works on any targeting rule.

```python
when { user.id % 100 < 20 } serve treatment rollout 50
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

### Explainability

Every evaluation produces a full decision trace.

```go
eval := flags.Explain("checkout_v2", ctx)
// eval.Variant: "treatment"
// eval.Reason: "segment:beta_users"
// eval.Segments: ["beta_users": true]
// eval.Chain: [prerequisite → segment → rollout → variant]
```

## Before / After

**Before** — 30 lines of Arishem JSON:

```json
{
  "OpLogic": "&&",
  "Conditions": [
    {
      "Operator": ">=",
      "Lhs": {"VarExpr": "user.cart_total"},
      "Rhs": {"Const": {"NumConst": 35}}
    },
    {
      "Operator": "!=",
      "Lhs": {"VarExpr": "user.region"},
      "Rhs": {"Const": {"StrConst": "XX"}}
    }
  ]
}
```

**After** — arbiter:

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

## Install

```bash
go install github.com/odvcencio/arbiter/cmd/arbiter@latest
```

## Usage

### CLI

```bash
# Compile and show stats
arbiter compile rules.arb

# Compile and evaluate against data
arbiter eval rules.arb --data '{"user": {"cart_total": 50, "region": "US"}}'

# Emit Arishem JSON (backward compatible)
arbiter emit rules.arb

# Emit a single rule's condition
arbiter emit rules.arb --rule FreeShipping

# Validate without emitting
arbiter check rules.arb
```

### Go Library — Rules

```go
// Compile from .arb source
ruleset, err := arbiter.Compile(source)

// Or compile from existing Arishem JSON (drop-in migration)
ruleset, err := arbiter.CompileJSONRules([]arbiter.JSONRule{{
    Name:      "FreeShipping",
    Priority:  1,
    Condition: condJSON,
    Action:    actJSON,
}})

// Create data context
dc := arbiter.DataFromMap(map[string]any{
    "user": map[string]any{
        "cart_total": 50.0,
        "region":     "US",
    },
}, ruleset)

// Evaluate — ~204ns per rule
matched, err := arbiter.Eval(ruleset, dc)
for _, m := range matched {
    fmt.Printf("%s → %s %v\n", m.Name, m.Action, m.Params)
}

// Or evaluate with debug trace
debug := arbiter.EvalDebug(ruleset, dc)
fmt.Printf("matched: %d, failed: %d, elapsed: %s\n",
    len(debug.Matched), len(debug.Failed), debug.Elapsed)
```

### Go Library — Flags

```go
f, err := flags.Load(source)

// Simple evaluation
variant := f.Variant("checkout_v2", ctx)
fmt.Println(variant.Name) // "treatment"

// Decode typed payload
var config CheckoutConfig
variant.Decode(&config)

// Full explainability
eval := f.Explain("checkout_v2", ctx)

// Hot reload from file
f, _ = flags.Watch("flags.arb")

// Serve over HTTP
http.Handle("/flags", f.Handler())
```

### Migrating from Arishem

```go
// Before (Arishem — 7.8GB for 10K rules)
rule, _ := arishem.NewPriorityRule(name, priority, condJSON, actJSON)
dc, _ := arishem.DataContext(ctx, inputJSON)
arishem.ExecuteRules([]arishem.RuleTarget{rule}, dc)

// After (Arbiter — 72MB for 10K rules, ~204ns/rule eval)
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
    when {
        # condition expression
    }
    then ActionName {
        key: value,
    }
    otherwise FallbackAction {
        key: value,
    }
}
```

`priority` is optional. `otherwise` is optional.

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
flags/        Feature flags, segments, rollouts, kill switches
emit/         Code generators: Rego, CEL, Drools DRL
decompile/    Bytecode → Arishem JSON
```

Flat `[]byte` of fixed-width 4-byte instructions: `[opcode(1B), flags(1B), arg(2B)]`. Constant pool indices are `uint16`, giving 65K unique values per type. The tree-sitter grammar gives any supporting editor syntax highlighting, folding, and structural navigation for `.arb` files.

## Examples

### E-commerce Pricing

```python
feature user from "user-service" {
    tier: string
    cart_total: number
    purchase_count: number
    is_first_order: bool
}

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
feature tx from "transaction-service" {
    amount: number
    ip_country: string
}

feature account from "account-service" {
    country: string
    has_2fa: bool
    flagged: bool
}

feature model from "fraud-model" {
    risk_score: number
}

rule InstantBlock priority 0 {
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
    when {
        tx.ip_country != account.country
        and tx.amount > 100
        and account.has_2fa == false
    }
    then Challenge {
        type: "sms_otp",
        timeout: "5m",
    }
}
```

### Content Moderation

```python
feature model from "ml-scorer" {
    toxicity: number
    spam_score: number
}

feature user from "user-service" {
    trust_score: number
    violations: number
}

const TRUSTED_THRESHOLD = 85

rule AllowTrusted priority 10 {
    when {
        user.trust_score >= TRUSTED_THRESHOLD
        and user.violations == 0
        and model.toxicity < 0.3
    }
    then Approve {
        fast_track: true,
    }
}
```

## License

Apache 2.0
