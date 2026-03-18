# arbiter

Rule engine DSL that transpiles `.arb` files to [Arishem](https://github.com/bytedance/arishem)-compatible JSON.

Write business rules as readable expressions. Get Arishem JSON ASTs that evaluate at microsecond latency.

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

**After** — 4 lines of arbiter:

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

```bash
# Emit all rules as JSON
arbiter emit rules.arb

# Emit a single rule's condition
arbiter emit rules.arb --rule FreeShipping

# Validate without emitting
arbiter check rules.arb
```

## Language

### Features

Declare the data your rules evaluate against. Features are fetched at runtime by Arishem.

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

Named values inlined at transpile time.

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

### Operator Mapping

| arbiter | Arishem |
|---|---|
| `==` `!=` `>` `<` `>=` `<=` | same |
| `and` | `OpLogic: "&&"` |
| `or` | `OpLogic: "\|\|"` |
| `not` | `OpLogic: "not"` |
| `in` / `not in` | `LIST_IN` / `!LIST_IN` |
| `contains` / `not contains` | `LIST_CONTAINS` / `!LIST_CONTAINS` |
| `retains` / `not retains` | `LIST_RETAIN` / `!LIST_RETAIN` |
| `subset_of` | `SUB_LIST_IN` |
| `superset_of` | `SUB_LIST_CONTAINS` |
| `vague_contains` | `LIST_VAGUE_CONTAINS` |
| `starts_with` | `STRING_START_WITH` |
| `ends_with` | `STRING_END_WITH` |
| `matches` | `CONTAIN_REGEXP` |
| `is null` / `is not null` | `IS_NULL` / `!IS_NULL` |
| `between [a, b]` | `BETWEEN_ALL_CLOSE` |
| `between (a, b)` | `BETWEEN_ALL_OPEN` |
| `between (a, b]` | `BETWEEN_LEFT_OPEN_RIGHT_CLOSE` |
| `between [a, b)` | `BETWEEN_LEFT_CLOSE_RIGHT_OPEN` |
| `any` | `FOREACH` with `OpLogic: "\|\|"` |
| `all` | `FOREACH` with `OpLogic: "&&"` |
| `none` | `FOREACH` with `OpLogic: "!\|\|"` |

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

rule DefaultReview priority 99 {
    when { true }
    then HoldForReview {
        queue: "general",
        sla: "4h",
    }
}
```

## How It Works

arbiter is built on [gotreesitter](https://github.com/odvcencio/gotreesitter), a pure-Go tree-sitter implementation. The grammar defines ~60 rules for the arbiter DSL. The transpiler walks the concrete syntax tree and emits Arishem JSON.

```
.arb source → gotreesitter parser → CST → transpiler → Arishem JSON
```

No generated files. No runtime dependency. The JSON goes straight to `arishem.JudgeCondition()`.

## License

Apache 2.0
