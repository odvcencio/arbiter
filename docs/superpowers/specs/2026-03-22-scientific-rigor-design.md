# Governance with Scientific Rigor

Arbiter is becoming more focused, more opinionated, and taking itself more seriously. This spec covers seven features that close the gaps where rigor was implicit but not enforced — making bundles self-describing, dimensionally aware, temporally expressive, testable, explorable, and editable with full language intelligence.

**Design principle**: Schemas are the gravitational center. Every other feature references them. The unit table is the standard library — facts about the physical world that ship with every bundle. Tests are the proof. The LSP and explorer are views into the same semantic model.

**Implementation order**: Schemas → Quantities/Units → Temporal operators → Join sugar → Test suites → LSP → Bundle explorer. Each layer builds on concrete foundations from the prior layer.

---

## 1. Fact & Outcome Schemas

First-class declarations that define the structure of facts and outcomes.

### Syntax

```arb
fact PlantStress {
  key:        string
  level:      string
  deficit_mm: number
  recorded:   timestamp
}

outcome WaterAction {
  zone:   string
  liters: number
}
```

`fact` and `outcome` are top-level keywords. Fields are `name: type`. Base types: `string`, `number`, `boolean`, `timestamp`. Optional fields use `?`:

```arb
fact PlantStress {
  key:        string
  level:      string
  deficit_mm: number
  note?:      string
}
```

### Semantics

- **Compile-time**: Rule bodies that reference `facts.PlantStress` are checked against the schema. Accessing `.nonexistent_field` is a compile error. Type mismatches (`deficit_mm == "high"`) are compile errors.
- **Runtime**: Every `assert` and `modify` in the expert loop validates field types against the schema. Cost is ~1–5 ns per field — negligible in the microsecond-band expert loop. Assertion of an unschema'd fact type is allowed (backward compat) but emits a warning in trace output. The compiled expert program carries the schema registry so it can validate at assertion time.
- **Outcomes**: `emit` actions are validated against outcome schemas identically. `emit WaterAction { zone: 42 }` is a type error.

### The `key` field

Every fact schema has an implicit `key: string` field. It does not need to be declared — it is always present, always required, always the identity field. This matches the existing runtime where `Fact.Key` is mandatory (assertion fails if empty). The `key` field is used for:
- Fact identity in working memory (type + key = unique fact)
- Self-join identity exclusion (automatic `a.key != b.key`)
- Retract/modify targeting

If a schema explicitly declares `key: string`, that is a no-op (not an error). Declaring `key` with any other type is a compile error.

### The `timestamp` type

`timestamp` fields store instants in time. Literal syntax uses ISO 8601:

```arb
const CUTOFF = 2026-01-01T00:00:00Z
```

Operations on timestamps:
- **Comparison**: `recorded > CUTOFF` — compares instants
- **Subtraction**: `now() - recorded` — produces a `Duration` value
- **Addition with duration**: `recorded + 5m` — produces a new timestamp

Timestamps interact with temporal operators: the `for`, `within`, and `cooldown` durations are measured against the session clock, which is itself a timestamp source. The synthetic fields `__asserted_at` and `__age_seconds` on facts remain available and are typed as `timestamp` and `number<time>` respectively in the schema-aware world.

Runtime representation: `int64` (Unix nanoseconds). Fits in the existing `Value.Num` field via float64 for the stateless path (sufficient precision for timestamps within ~285 years of epoch) or `Value.Any` for nanosecond-exact expert session use.

### Relationship to `feature` declarations

Existing `feature` declarations describe input data shape — what the caller provides. `fact` and `outcome` schemas describe expert system data shape — what the engine asserts and emits internally. They are complementary, not overlapping:

- `feature user { name: string, age: number }` → describes the input context
- `fact CreditScore { key: string, score: number }` → describes derived working memory
- `outcome Recommendation { action: string }` → describes emitted results

Features remain as-is. No deprecation.

### IR Representation

```go
type FactSchema struct {
    Name   string
    Fields []SchemaField
    Span   Span
}

type OutcomeSchema struct {
    Name   string
    Fields []SchemaField
    Span   Span
}

type SchemaField struct {
    Name     string
    Type     FieldType
    Required bool       // default true; optional fields marked with ?
    Span     Span
}

// FieldType represents a schema field's type, including parameterized types.
type FieldType struct {
    Base      string  // "string", "number", "boolean", "timestamp", "decimal"
    Dimension string  // "" for unparameterized, "temperature"/"currency"/etc. for number<dim>/decimal<dim>
}
```

Added to `ir.Program`:

```go
type Program struct {
    FactSchemas    []FactSchema
    OutcomeSchemas []OutcomeSchema
    // ... existing fields
}
```

---

## 2. Quantities, Units & Decimal

### Quantities

Constants and schema fields can carry units:

```arb
const SAFE_TEMP    = 28 C
const MAX_CO2      = 1200 ppm
const FLOW_RATE    = 2.5 L/min

fact SensorReading {
  key:         string
  temperature: number<temperature>
  humidity:    number<percentage>
  co2:         number<concentration>
}
```

Inline in expressions:

```arb
when { reading.temperature > 28 C }
when { reading.co2 > 1200 ppm }
```

### Type system extension

`number` remains valid (dimensionless). `number<dimension>` is a quantity-typed field. Scientific notation is supported: `6.022e23`, `1.381e-23`.

```
FieldType = "string" | "number" | "boolean" | "timestamp"
          | "number<" dimension ">"
          | "decimal" | "decimal<" dimension ">"
```

### The unit table (standard library)

Built-in, immutable, ships with Arbiter. Not user-extensible in v1 — additions come through Arbiter releases.

| Dimension | Units | Base Unit |
|-----------|-------|-----------|
| temperature | C, F, K | K |
| length | mm, cm, m, km, in, ft, yd, mi | m |
| mass | mg, g, kg, lb, oz | kg |
| time | ms, s, min, hr, d | s |
| volume | mL, L, gal, fl_oz | L |
| pressure | Pa, hPa, kPa, bar, psi, atm | Pa |
| percentage | pct (or %) | pct |
| concentration | ppm, ppb | ppm |
| area | mm2, cm2, m2, km2, ha, acre | m2 |
| speed | m/s, km/h, mph, kn | m/s |
| electric_current | mA, A | A |
| voltage | mV, V, kV | V |
| power | W, kW, MW, hp | W |
| energy | J, kJ, kWh, cal, kcal | J |
| frequency | Hz, kHz, MHz, GHz | Hz |
| data | B, KB, MB, GB, TB | B |
| flow | L/min, L/hr, gal/min, m3/s | L/min |
| currency | USD, EUR, GBP, JPY, CNY, CHF, CAD, AUD, ... | none (no auto-conversion) |
| cryptocurrency | BTC, ETH, SOL, USDC, USDT, ... | none (no auto-conversion) |

Currency and cryptocurrency dimensions have **no built-in conversion factors**. Exchange rates are runtime facts, not constants. The compiler enforces dimension compatibility (`USD + EUR` is a compile error) but conversions are the bundle author's responsibility.

### Compile-time checks

- **Dimension mismatch**: `temperature > 1200 ppm` → compile error
- **Same-dimension, different unit**: `28 C > 82 F` → allowed, compiler inserts conversion to base unit
- **Dimensionless vs quantity**: literal needs a unit if the field is quantity-typed

### Runtime representation

Quantities: `(float64, unitID)` pair. Comparisons normalize to base unit. Conversion factors are a compile-time lookup table.

```go
type Quantity struct {
    Value float64
    Unit  uint16  // index into unit table
}

type UnitEntry struct {
    Symbol    string
    Dimension uint8
    ToBase    float64
    FromBase  float64
    Offset    float64  // for C→K, F→K non-linear conversions
}
```

Conversion formula: `base = value * ToBase + Offset`. For most dimensions, `Offset` is 0 (pure scaling). For temperature, `ToBase` and `Offset` are precomputed combined constants:
- Celsius → Kelvin: `ToBase = 1.0, Offset = 273.15`
- Fahrenheit → Kelvin: `ToBase = 5/9 (0.5556), Offset = 255.372` (precomputed from `(-32 × 5/9) + 273.15`)

Derived units (rates) are `X/Y` — the compiler validates both halves. No arbitrary derived unit algebra.

### The `decimal` type

For money and cryptocurrency. Exact arithmetic, no floating-point drift.

```arb
fact Transaction {
  key:      string
  amount:   decimal<currency>
  gas_fee:  decimal<currency>
}

const LIMIT   = 10000.00 USD
const GAS_CAP = 0.000000000000000001 ETH  // 1 wei
```

- `decimal` is a new base type alongside `number`. `number` stays `float64` for science. `decimal` is exact for money.
- Internally: `(int128, scale)` — a scaled integer. `10000.00 USD` is `(1000000, 2)`. Covers 38 significant digits — enough for any monetary value including wei-denominated ETH.
- `int128` is two `int64`s. No external dependency.
- **Type isolation**: `decimal` vs `number` arithmetic is a compile error. No implicit coercion. Scientists don't accidentally mix sensor floats with money.

### Quantity arithmetic rules

Quantities support comparison and same-dimension arithmetic. They do not support cross-dimension algebra.

- **Comparison**: `28 C > 20 C` — allowed (same dimension). `28 C > 1200 ppm` — compile error.
- **Same-dimension add/subtract**: `5 m + 3 m` → `8 m`. `deficit_mm - 2 mm` → valid. Both operands must share a dimension; result inherits that dimension.
- **Scalar multiply/divide**: `deficit_mm * 2` → valid (scales the quantity). `10 L / 5` → `2 L`.
- **Cross-dimension multiply/divide**: `5 m * 3 m` → compile error. Arbiter does not derive new dimensions from arithmetic. Derived dimensions like `area` and `speed` are declared in the unit table, not computed.
- **Decimal arithmetic**: same rules. `500 USD + 100 USD` → `600 USD`. `500 USD + 100 EUR` → compile error.

This keeps dimensional analysis to comparison and scaling — enough to stop nonsense without a symbolic algebra engine.

### Expression graph integration

Quantities and decimals integrate into the existing flat `Expr` array via new `ExprKind` variants:

```go
// New ExprKind values
ExprQuantityLit  // number + unit: 28 C, 1200 ppm
ExprDecimalLit   // exact decimal + optional unit: 10000.00 USD, 0.5

// Extended Expr fields
type Expr struct {
    // ... existing fields (Kind, Number, Str, Bool, etc.)
    Unit         string  // unit symbol for ExprQuantityLit / ExprDecimalLit
    DecimalHi    int64   // high 64 bits of int128 for ExprDecimalLit
    DecimalLo    int64   // low 64 bits of int128 for ExprDecimalLit
    DecimalScale int32   // decimal scale for ExprDecimalLit
}
```

`ExprQuantityLit` reuses the existing `Number` (float64) field and adds `Unit`. `ExprDecimalLit` uses `DecimalHi`/`DecimalLo`/`DecimalScale` and `Unit`.

### Compile-time normalization strategy (VM path)

For the **stateless rule VM** (~200ns path): quantities are normalized to base units at compile time. `28 C` becomes `301.15` (Kelvin) in the number pool. The VM never sees units — it compares plain float64s. Dimension checking is purely compile-time. This means zero runtime overhead for the fast path.

For the **expert session**: quantities arrive as runtime facts through the Go API. The `Fact.Fields` map uses a `Quantity` struct to carry unit information:

```go
// Go API for asserting quantity-typed facts
session.Assert(expert.Fact{
    Type: "SensorReading",
    Key:  "zone-A",
    Fields: map[string]any{
        "temperature": expert.Q(28, "C"),   // Quantity{Value: 28, Unit: "C"}
        "humidity":    expert.Q(65, "pct"),
    },
})
```

`expert.Q(value, unit)` is a convenience constructor returning a `Quantity`. On assertion, the runtime validates the field against the schema: checks that the unit belongs to the expected dimension, then normalizes to base unit for internal storage and comparison. If a plain `float64` is asserted for a quantity field, it is treated as already in base units (backward compat with a warning).

For **decimal fields**, the Go API uses `expert.D(value)`:

```go
session.Assert(expert.Fact{
    Type: "Transaction",
    Key:  "tx-001",
    Fields: map[string]any{
        "amount": expert.D("10000.00", "USD"),  // parses string to int128 + scale
    },
})
```

String input avoids float64 precision loss. `expert.D` parses the decimal string into `(int128, scale)` at assertion time.

### VM value type impact

Adding decimal to the VM `Value` struct would bloat it (two extra int64s). Instead, decimal values use the existing `Any` field for the expert session path. The stateless VM path does not handle decimal — decimal-typed rules must go through expert evaluation. This is acceptable because decimal use cases (financial compliance, transaction limits) are inherently stateful — they need fact schemas, audit trails, and explainability, not 200ns stateless eval.

If a future use case demands stateless decimal rules, a dedicated `DecimalPool` (parallel to the number pool) can be added without changing the `Value` struct — the pool index fits in `uint16` like everything else.

---

## 3. Temporal Operators

Concise syntax for time-dependent judgment. Two categories based on what they modify.

### Condition-scoped operators

Attach to `when` clauses. Modify what "true" means.

**`for <duration>`** — condition must remain continuously true for the duration before the rule fires.

```arb
expert rule HeatStress priority 5 {
  when { reading.temperature > 28 C } for 10m
  then emit HeatWarning { zone: reading.zone, severity: "high" }
}
```

Timer resets if the condition becomes false at any point.

**`within <duration>`** — condition must become true at least once within a sliding window.

```arb
expert rule RecentSpike priority 3 {
  when { reading.co2 > 1200 ppm } within 5m
  then emit CO2Alert { zone: reading.zone }
}
```

Keeps the condition "active" for the window duration after it becomes false.

**`stable_for <N> cycles`** — condition must be true for N consecutive evaluation rounds (cycle-counted, not wall-clock).

```arb
expert rule ConfirmedTrend priority 4 {
  when { reading.humidity < 30 pct } stable_for 3 cycles
  then emit DryAlert { zone: reading.zone }
}
```

### Rule-scoped operators

Attach to the rule declaration alongside `priority`, `kill_switch`, etc. Throttle the action.

**`cooldown <duration>`** — after the rule fires, suppress it for the specified duration.

```arb
expert rule IrrigationPulse priority 5 cooldown 15m {
  when { stress.deficit_mm > 10 mm }
  then emit WaterAction { zone: stress.key, liters: stress.deficit_mm * 2 }
}
```

**`debounce <duration>`** — condition must be continuously true for the duration before firing, AND the rule won't fire again until the condition goes false and comes back true.

```arb
expert rule PressureDrop priority 3 debounce 30s {
  when { env.pressure < 950 hPa }
  then emit PressureAlert { level: "warning" }
}
```

### Duration syntax

Temporal durations use shorthand notation: `10m`, `5s`, `1hr`, `3d`. These are distinct from quantity expressions — they use a compact parser, not the unit table. The mapping:

| Shorthand | Unit table equivalent | Meaning |
|-----------|----------------------|---------|
| `ms` | `ms` | milliseconds |
| `s` | `s` | seconds |
| `m` | `min` | minutes |
| `hr` | `hr` | hours |
| `d` | `d` | days |

`m` in temporal context means minutes (not meters). The parser disambiguates by position: after `for`, `within`, `cooldown`, `debounce`, or `at T+`, a number-letter pair is a duration. In expression context (inside `when { ... }`), `m` is a unit from the unit table (meters). No ambiguity because temporal operators are syntactically outside the condition body.

### Combining temporal operators

`for` and `debounce` are **mutually exclusive** on the same rule — `debounce` already includes the "must be true for duration" semantics of `for`, plus single-shot behavior. Declaring both is a compile error.

Valid combinations:
- `for` + `cooldown`: condition must hold for duration, then suppress re-fire for cooldown period
- `within` + `cooldown`: react to transient events, then suppress for cooldown
- `stable_for` + `cooldown`: require N consecutive cycles, then suppress
- `debounce` alone: includes sustained-true + single-shot in one keyword
- `cooldown` alone: throttle without condition-time requirements

### Runtime model

Temporal state is per-rule, stored in the expert session:

```go
type TemporalState struct {
    ConditionTrueAt  time.Time  // for `for`
    LastFiredAt      time.Time  // for `cooldown`
    CycleCount       int        // for `stable_for`
    WithinExpiresAt  time.Time  // for `within`
    DebounceArmed    bool       // for `debounce`
}
```

Rules without temporal operators have zero overhead — no state allocated, no checks.

### IR representation

Fields on the existing `ExpertRule` node:

```go
type ExpertRule struct {
    // ... existing fields
    ForDuration      *Duration
    WithinDuration   *Duration
    StableCycles     *int
    CooldownDuration *Duration
    DebounceDuration *Duration
}

type Duration struct {
    Value float64
    Unit  string   // "ms", "s", "m", "hr", "d" — temporal shorthand
    Span  Span
}
```

---

## 4. Relation Sugar (Join Syntax)

Zero new runtime semantics. Compiler transform to nested quantifiers.

### Syntax

```arb
expert rule CrossZoneAlert priority 5 {
  when {
    join s: Sensor, z: Zone on s.zone_id == z.id {
      s.temperature > z.threshold
    }
  }
  then emit ZoneAlert { zone: z.name, temp: s.temperature }
}
```

Multiple join predicates:

```arb
when {
  join r: Reading, s: Sensor, z: Zone
    on r.sensor_id == s.id and s.zone_id == z.id
  {
    r.value > z.max_allowed
  }
}
```

### Shorthand for same-named fields

```arb
join s: Sensor, z: Zone on .zone_id { ... }
// desugars to: on s.zone_id == z.zone_id
```

Only works with exactly two bindings.

### Desugaring

```arb
join s: Sensor, z: Zone on s.zone_id == z.id { body }

// becomes:
any s in facts.Sensor {
  any z in facts.Zone {
    s.zone_id == z.id and body
  }
}
```

### Self-joins

Supported with automatic identity exclusion via `key`:

```arb
join a: Sensor, b: Sensor on a.zone_id == b.zone_id {
  abs(a.temperature - b.temperature) > 5 C
}

// desugars to:
any a in facts.Sensor {
  any b in facts.Sensor {
    a.key != b.key and a.zone_id == b.zone_id and abs(a.temperature - b.temperature) > 5 C
  }
}
```

Override with explicit `including_self` for reflexive matching:

```arb
join a: Sensor, b: Sensor including_self on a.zone_id == b.zone_id {
  a.reading_count + b.reading_count
}
```

`including_self` suppresses the automatic `a.key != b.key` injection. Placed after the binding list, before `on`.

### Built-in functions

The self-join example uses `abs()`. Arbiter provides a small set of built-in numeric functions:

- `abs(x)` — absolute value
- `min(x, y)` — minimum
- `max(x, y)` — maximum
- `round(x)` — round to nearest integer
- `floor(x)` — round down
- `ceil(x)` — round up

These are a new `ExprKind` variant (`ExprBuiltinCall`) compiling to dedicated opcodes. The set is intentionally small — Arbiter is not a general-purpose language.

Additionally, `now()` is a zero-argument built-in that reads the session clock and returns a `timestamp`. It does not appear in the function list above because it has no arguments and has side-channel semantics (reads clock state), but it is an `ExprBuiltinCall` with `FuncName: "now"` and zero args.

Expression graph fields for built-in calls:

```go
// Added to Expr struct
type Expr struct {
    // ... existing fields + quantity/decimal fields from Section 2
    FuncName string    // for ExprBuiltinCall: "abs", "min", "max", "round", "floor", "ceil", "now"
    Args     []ExprID  // for ExprBuiltinCall: argument expression references
}
```

Built-in functions preserve quantity dimensions: `abs(-5 C)` → `5 C`. `min(28 C, 82 F)` → works (same dimension, normalized to base unit for comparison, result in the first operand's unit).

### IR representation

No new IR node for joins. The lowering step emits nested `any` quantifiers with the `on` predicate folded into the body. By the time the IR exists, joins are gone.

### Join complexity note

An N-way join desugars to N nested quantifiers, giving O(n₁ × n₂ × ... × nₖ) iterations of the inner body per evaluation round. The expert session's dirty-fact tracking skips re-evaluation of entire rules when no relevant facts changed, but does not short-circuit inner iterations. For large fact sets, authors should prefer narrow schemas and selective `on` predicates. The typical scientific use case (tens to hundreds of facts per type, not thousands) is well within budget.

### Constraints

- Bindings must reference declared fact schemas
- `on` predicate must reference fields from bound variables
- At least two bindings required
- Self-joins inject implicit `a.key != b.key` (unless `including_self`)
- `including_self` only valid on self-joins (same fact type in two or more bindings)

---

## 5. Test Suites (`*.test.arb`)

Executable specs. Test files live alongside their bundle and cover all four modalities.

### File convention

```
bundle/
  pricing.arb
  pricing.test.arb
  irrigation.arb
  irrigation.test.arb
```

A `*.test.arb` file tests the `.arb` file with the matching name. No explicit import. The test compiles the full bundle — if `pricing.arb` includes shared segments or other files, the test sees the complete compiled result, not just declarations from `pricing.arb`.

### Assertion tests (stateless rules and flags)

```arb
test "free shipping for high-value customers" {
  given {
    user.lifetime_spend: 15000
    user.cart_total: 50
  }
  expect rule FreeShipping matched
  expect action ApplyShipping { cost: 0, method: "standard" }
}

test "checkout flag routes beta users" {
  given {
    user.segment: "beta"
  }
  expect flag checkout_v2 == "treatment"
}

test "low cart value denied" {
  given {
    user.lifetime_spend: 15000
    user.cart_total: 20
  }
  expect rule FreeShipping not matched
}
```

### Scenario tests (expert sessions)

```arb
scenario "tax computation chain" {
  given {
    assert Income { key: "taxpayer-1", wages: 75000 USD, interest: 1200 USD }
  }

  after stabilization {
    expect fact AGI { key: "total", amount: 76200 USD }
    expect fact TaxableIncome { amount: 63400 USD }
    expect outcome TaxLiability { amount: 9442 USD }
  }
}
```

`after stabilization` — run the expert loop until quiescent, then check.

### Temporal scenario tests

```arb
scenario "sustained heat triggers alert" {
  at T+0 {
    assert SensorReading { key: "zone-A", temperature: 30 C }
  }

  at T+5m {
    assert SensorReading { key: "zone-A", temperature: 31 C }
    expect no outcome HeatWarning
  }

  at T+10m {
    assert SensorReading { key: "zone-A", temperature: 29 C }
    expect outcome HeatWarning { zone: "zone-A", severity: "high" }
  }
}

scenario "cooldown prevents rapid re-fire" {
  at T+0 {
    assert PlantStress { key: "zone-1", deficit_mm: 15 mm }
  }
  after stabilization {
    expect outcome WaterAction { zone: "zone-1" }
  }

  at T+5m {
    assert PlantStress { key: "zone-1", deficit_mm: 20 mm }
  }
  after stabilization {
    expect no outcome WaterAction
  }

  at T+16m {
    assert PlantStress { key: "zone-1", deficit_mm: 18 mm }
  }
  after stabilization {
    expect outcome WaterAction { zone: "zone-1" }
  }
}
```

`at T+<duration>` advances the deterministic clock.

### Continuous arbiter tests

```arb
scenario "fraud monitor triggers on velocity" {
  stream event { type: "purchase", amount: 500 USD, user: "u-123" }
  stream event { type: "purchase", amount: 600 USD, user: "u-123" }
  stream event { type: "purchase", amount: 700 USD, user: "u-123" }

  within 1m {
    expect outcome FraudAlert { user: "u-123", reason: "velocity" }
  }
}
```

### Expect operators

```arb
expect rule <name> matched
expect rule <name> not matched
expect action <name> { field: value, ... }
expect flag <name> == <value>
expect fact <type> { field: value, ... }
expect no fact <type> { key: <value> }
expect outcome <type> { field: value, ... }
expect no outcome <type>
expect outcome <type> { liters: > 50 }
expect outcome <type> { temp: between 20 C 30 C }
```

### Execution

```bash
arbiter test                        # all *.test.arb files
arbiter test irrigation.test.arb    # specific file
arbiter test --verbose              # full trace per test
```

### IR representation

```go
type TestSuite struct {
    Source    string
    Tests     []TestCase
    Scenarios []Scenario
}

type TestCase struct {
    Name         string
    Given        []GivenEntry
    Expectations []Expectation
    Span         Span
}

type Scenario struct {
    Name  string
    Steps []ScenarioStep
    Span  Span
}

type ScenarioStep struct {
    AtTime       *Duration
    Stabilize    bool
    Assertions   []FactAssertion
    StreamEvents []StreamEvent
    Expectations []Expectation
}

type Expectation struct {
    Kind    ExpectKind  // rule_matched, action, flag, fact, outcome, etc.
    Target  string
    Fields  map[string]ExpectValue
    Negated bool
}

// ExpectValue represents a field expectation — either an exact match or a comparison.
type ExpectValue struct {
    Kind     ExpectValueKind  // exact, gt, gte, lt, lte, between
    Value    ExprID           // the expected value (literal expression)
    HighValue ExprID          // upper bound for `between`
}

// ExpectValueKind determines how a field expectation is matched.
const (
    ExpectExact   ExpectValueKind = iota  // field == value
    ExpectGt                               // field > value
    ExpectGte                              // field >= value
    ExpectLt                               // field < value
    ExpectLte                              // field <= value
    ExpectBetween                          // value <= field <= high_value
)

// StreamEvent represents a simulated event for continuous arbiter tests.
type StreamEvent struct {
    Name   string              // event label (matches arbiter's stream source)
    Fields map[string]ExprID   // event payload fields as literal expressions
    Span   Span
}

// GivenEntry represents a key-value pair in a test's `given` block (data context).
type GivenEntry struct {
    Path  string  // dotted path, e.g. "user.lifetime_spend"
    Value ExprID  // literal value expression
    Span  Span
}

// FactAssertion represents an `assert` statement in a scenario step.
type FactAssertion struct {
    FactType string             // schema name, e.g. "SensorReading"
    Fields   map[string]ExprID  // field values as literal expressions
    Span     Span
}
```

Test IR is separate from the bundle IR — references the bundle but doesn't affect compilation.

---

## 6. Language Server Protocol (LSP)

B-tier now (diagnostics, completions, go-to-definition, hover), with internals that make C-tier (cross-bundle awareness) an extension, not a rewrite.

### Architecture

```
Editor (VS Code, Neovim, etc.)
  ↕ JSON-RPC (stdio)
arbiter lsp
  ↕
Semantic Model
  ├── Parser (gotreesitter — incremental re-parse)
  ├── IR (lowered from CST per file)
  ├── Symbol Table (names → definitions → types)
  └── Unit Table (built-in standard library)
```

Subcommand of the arbiter binary. Single binary, no sidecar.

### Semantic model

```go
type SemanticModel struct {
    Files    map[string]*FileState
    Symbols  *SymbolTable
    Units    *UnitTable
    Schemas  map[string]*Schema
}

type FileState struct {
    URI      string
    Source   []byte
    Tree     *treesitter.Tree  // incremental CST
    IR       *ir.Program
    Diags    []Diagnostic
    Version  int
}
```

On file change: incremental re-parse → re-lower to IR (error-tolerant mode) → re-run diagnostics → update symbol table → push diagnostics.

The current `Lower()` function rejects files with parse errors. The LSP needs an error-tolerant lowering mode that produces a partial IR from the valid portions of the CST, skipping `ERROR`/`MISSING` subtrees. This is standard for language servers — partial results enable diagnostics and completions even while the user is mid-edit.

### Diagnostics

**Parse errors**: free from gotreesitter's error recovery (`ERROR`/`MISSING` CST nodes).

**Semantic errors**:
- Unknown fact/outcome type
- Unknown field on a schema'd type
- Type mismatch in comparisons
- Dimension mismatch in quantity comparisons
- Decimal/number mixing
- Missing required field in assert
- Invalid temporal operator placement

**Warnings**: unschema'd fact types, unreachable rules, unused segments.

### Completions

Context-aware:
- **Top-level**: `rule`, `expert`, `flag`, `segment`, `fact`, `outcome`, `const`, `arbiter`, `feature`, `test`, `scenario`
- **`facts.`**: declared fact schema names
- **`facts.Sensor.`**: fields from schema
- **After quantity field**: compatible units from unit table
- **`then` clause**: `assert`/`emit`/`retract`/`modify` → schema names → field names
- **`join` bindings**: fact schema names, then fields from bound schemas
- **Rule-level**: `priority`, `cooldown`, `debounce`, `kill_switch`

### Go-to-definition

| Usage | Target |
|-------|--------|
| `facts.PlantStress` | `fact PlantStress { ... }` |
| `segment high_value` in rule | `segment high_value { ... }` |
| `requires BasicRiskCheck` | `rule BasicRiskCheck { ... }` |
| `SAFE_TEMP` in expression | `const SAFE_TEMP = 28 C` |
| `emit WaterAction` | `outcome WaterAction { ... }` |
| `s.temperature` | field line in schema |

### Hover

| Target | Shows |
|--------|-------|
| Field `s.temperature` | `temperature: number<temperature>` |
| Unit literal `28 C` | `28 °C (temperature) = 301.15 K` |
| Rule name | priority, temporal operators, prereqs, kill_switch |
| Schema name | full schema with fields and types |
| `join` binding | bound schema and fields |
| Temporal `for 10m` | "Condition must remain true for 10 minutes continuously" |

### Road to C-tier (cross-bundle awareness)

The path from B to C:
1. **Workspace model**: `arbiter.workspace` file listing bundle files in a project
2. **Cross-file symbol table**: merged symbols with provenance tracking
3. **Cross-reference queries**: "who asserts PlantStress?", "who consumes WaterAction?"
4. **Bundle-level diagnostics**: asserted but never consumed facts, emitted but never tested outcomes

Current design enables this: `SemanticModel.Files` is a map (expands to workspace), symbols carry provenance (which file, which span). Cross-file queries are lookups, not re-parses.

### Test file support

`*.test.arb` files are first-class in the LSP. Schema-aware completions in `given`/`assert` blocks. Completions for rule/fact/outcome names in `expect` blocks. Go-to-definition from test expectations to bundle declarations.

---

## 7. Bundle Explorer

An interactive semantics viewer. A lightbox for experiments — hold a decision up to the light and see every derivation, every fact, every temporal state that led to it.

### Launching

```bash
arbiter explore                          # local, current directory
arbiter explore ./bundles/irrigation.arb # specific bundle
arbiter explore --serve :8080            # server mode, connect to gRPC
```

Local mode compiles and loads from disk. Server mode connects to a running Arbiter instance via gRPC. Same frontend, different data source. Users own deployment and security.

### Capabilities

**1. Bundle inspection**

Landing view. Auto-generated from IR:
- Schemas (fact and outcome, with field types and units)
- Constants (with unit annotations)
- Segments, rules (with priority, governance, temporal operators)
- Expert rules (with action kinds, activation groups)
- Flags (with variants, rollout percentages)
- Continuous arbiters (with poll/stream config)
- Unit table subset (dimensions/units this bundle uses)

**2. Interactive evaluation**

- Schema-generated input forms. `deficit_mm: number<length>` renders as a numeric field with unit selector scoped to length dimension.
- Choose modality: evaluate rules, resolve flag, run expert session, simulate arbiter.
- See matched rules, emitted outcomes, resolved flags — typed and annotated.

**3. Explainability trace**

- Rule evaluation order, pass/fail with actual values
- Expert inference chain: fact → rule → outcome derivation graph
- Temporal state: condition durations, cooldown timers, cycle counts

**4. Diff: active vs candidate**

- Structural diff: schema, rule, flag, governance changes
- Behavioral diff: given same inputs, show where outcomes diverge
- Test cases double as diff inputs

**5. Time simulation**

- Timeline scrubber advancing the deterministic clock
- Inject facts at specific times
- Watch temporal state evolve
- Step by cycle or wall-clock increment
- Replay recorded fact sequences

**6. Override sandbox**

- Kill switch toggles, rollout adjustments, flag overrides, priority overrides
- Before/after comparison

**7. Health and freshness (continuous arbiters)**

- Poll timing, stream connection status, checkpoint state
- Staleness indicators, handler invocation history

**8. Decision replay**

The lightbox. Load an audit record (inputs + timestamp + context):
- Re-evaluate with current bundle — same result?
- Re-evaluate with candidate bundle — what would have changed?
- Closes the loop: production decision → audit → replay → verify

### Frontend architecture

- SPA embedded as static assets in the arbiter binary via Go `embed`
- No CDN, no external dependencies, no build step for the user
- API layer: Go HTTP server wrapping the same eval/compile/expert APIs
- Local mode: in-process. Server mode: proxy to gRPC.

### Backend API surface

| Capability | Status |
|-----------|--------|
| Compile bundle, return IR | Exists |
| Evaluate rules with data context | Exists |
| Resolve flag | Exists |
| Run expert session | Exists |
| Explainability trace | Exists |
| List schemas, constants, segments | New (IR introspection) |
| Diff two compiled bundles | New |
| Time simulation / clock control | Partially exists |
| Override application | Exists |
| Audit replay | New |

### Bundle diff engine

```go
type BundleDiff struct {
    Schemas   []SchemaDiff
    Rules     []RuleDiff
    Flags     []FlagDiff
    Experts   []ExpertDiff
    Constants []ConstDiff
}
```

Plus behavioral diff mode: given test inputs, evaluate both bundles, report divergences.
