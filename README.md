# arbiter

A compact language for governed outcomes.

Every decision your software makes — approve this transaction, show this variant, block this request, compute this tax bracket — is a governed outcome. Arbiter gives those decisions a language, compiles them to bytecode, and evaluates simple precompiled rules in the low hundreds of nanoseconds.

```text
.arb source ──→ Parser ──→ Compiler ──→ Bytecode VM (~200ns simple eval)
                              │
                              └──→ Arishem JSON (compatibility)
```

Four modalities, one language. **Stateless evaluation** for request-path decisions. **Feature flags** for governed variant resolution. **Expert inference** for forward-chaining reasoning until quiescence. **Continuous arbiters** for always-on decision loops. Same compiler, same VM, same governance.

The parser is built on [gotreesitter](https://github.com/odvcencio/gotreesitter), a pure-Go reimplementation of the tree-sitter runtime — no CGo, no C toolchain, no generated files. Cross-compiles to any `GOOS`/`GOARCH` target Go supports, including WASM.

Standalone reference material lives under [`docs/`](docs):

- [`docs/language/grammar.ebnf`](docs/language/grammar.ebnf) is the tooling-facing language specification.
- [`docs/architecture/compiler.md`](docs/architecture/compiler.md) explains the compiler pipeline, compiled ruleset layout, and the compile-time/runtime string-pool split.

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

Fixed 256-element stack. The current public benchmark path is low-allocation rather than zero-allocation: `96 B/op`, `3 allocs/op`. The constant pool interns all strings and numbers — 10K rules referencing the same field names share one copy.

To separate engine cost from transport cost, this repo also ships a split latency benchmark over the `fraud` example:

```bash
# Pure in-process governed eval
go test -run '^$' -bench '^BenchmarkLatencySplit/in_process_governed_eval$' -benchmem

# gRPC through a local kubectl port-forward
ARBITER_BENCH_PORT_FORWARD_ADDR=127.0.0.1:18081 \
go test -run '^$' -bench '^BenchmarkLatencySplit/grpc_port_forward$' -benchmem -benchtime=100x

# gRPC direct to the cluster service (run from an environment that can resolve it)
ARBITER_BENCH_IN_CLUSTER_ADDR=arbiter.orchard.svc.cluster.local:8081 \
go test -run '^$' -bench '^BenchmarkLatencySplit/grpc_in_cluster$' -benchmem -benchtime=100x
```

The gRPC benches publish the bundle once, warm it up, and benchmark `EvaluateRules` only.

## Governance

Segments, rollouts, kill switches, prerequisites, explainability — governance primitives that apply to any outcome. Rules, strategies, flags, and expert inferences all share them.

Within stateless governed evaluation, rules collect applicable outcomes, strategies select one ordered path, and flags resolve named variants.

### Rules

```arb
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

```arb
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

Reusable conditions. Define once, reference from any rule, strategy, or flag.

```arb
segment beta_users {
    user.cohort matches "^beta_"
}

segment high_value {
    user.lifetime_spend > 10000
}
```

### Strategies

Strategies handle ordered stateless governed evaluation over recognized decision shapes in current facts/state, with exactly-one routing and a required fallback.

```arb
outcome CheckoutPath {
    target: string
    reason: string
}

strategy CheckoutRouting returns CheckoutPath {
    when {
        risk.requires_review == true
    } then Manual {
        target: "manual",
        reason: "review required",
    }

    else Automatic {
        target: "auto",
        reason: "default path",
    }
}
```

They reuse the same conditions, segments, rollouts, and trace machinery as rules, but the evaluation model recognizes one named shape and then takes the first matching governed path with an explicit fallback.

### Feature Flags

Flags add one concept to the governance model: **variants** — named outcomes with typed payloads. Everything else (segments, rollouts, kill switches, prerequisites, explainability) is shared.

```arb
flag checkout_v2 type multivariate default "control" {
    owner: "growth"
    ticket: "ENG-1234"

    variant "treatment" {
        show_new_ui: true,
        layout: "single_page",
    }

    when beta_users then "treatment"
    when { user.country == "US" } rollout 50 then "treatment"
}
```

Schema validation, secret references, request-level caching, hot reload, HTTP serving, explainability traces — all come along.

### Expert Inference

Forward-chaining rules that reason until quiescence. Facts build on facts. Rules fire, assert new facts into working memory, and the engine loops until nothing changes.

```arb
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

Expert actions:

- `assert` inserts or updates a fact and can trigger more rule firings
- `emit` produces a final outcome without mutating working memory
- `retract` hides a fact by `type` and `key` while its rule remains supported
- `modify` overlays field updates on an existing fact with a `set { ... }` block while its rule remains supported

Expert controls:

- `kill_switch`, `requires`, and `rollout` work the same way they do for ordinary rules
- `no_loop` prevents a rule from re-firing solely because of its own mutations
- `activation_group name` allows only the first matching rule in a group to fire per round

The session runs with guardrails — configurable max rounds and max mutations, context cancellation. Every firing is recorded in the activation trace.

`modify` and `retract` are reversible overlays, not one-way destructive writes. If the supporting rule stops matching, the underlying fact view is recomputed and the overlay falls away. That can produce a steady-state no-op activation in the trace while a modifier or retractor remains active.

Temporal windows are available directly in the expert context. Facts expose `__round`, `__asserted_at`, and `__age_seconds`, and the session context exposes `current_round` plus `__now`. That lets long-lived sessions write age-based rules without extra scheduler glue:

```arb
expert rule EscalateStaleCase {
	when {
		any case in facts.Case {
			case.__age_seconds >= 3600
		}
	}
	then emit Escalate {
		key: case.key,
		age_seconds: case.__age_seconds,
	}
}
```

For deterministic tests or external schedulers, `expert.Options.Now` lets you inject the session clock instead of relying on `time.Now()`.

### Continuous Arbiters

Long-lived decision loops are first-class in `.arb` too. An `arbiter` declaration lives beside the rules it runs, so one bundle can define trigger modes, fact sources, outcome routing, persistence, and the decision logic itself.

```arb
arbiter trading_system {
    stream wss://exchange.com/prices
    schedule "0 8 * * MON-FRI" source https://calendar.api/market-hours
    checkpoint /var/lib/arbiter/trading.state

    on Opportunity where confidence > 0.8 chain ai_analysis
    on RiskAlert where severity == "critical" exec "kill-all-orders"
    on RiskAlert where severity == "warning" slack #trading-risk
    on * audit /var/log/trading.jsonl
}
```

The declaration surface is built around a few ideas:

- `poll 30s`, `schedule "cron expr"`, and `stream uri` are the three first-class trigger modes
- `source uri` declares external fact inputs, and `chain target_arbiter` declares that one arbiter's outcomes should feed another
- `on Outcome where ... handler target` routes by outcome fields, not just outcome name
- `checkpoint path` marks the arbiter as stateful across restarts

The runtime-side fact adapters already ship separately in `expert/factsource`. Today that includes `.csv`, `.json`, `.jsonl`, `http(s)://`, `gsheet://SPREADSHEET_ID/SheetName`, versioned `postgres://...` tables, and Terraform/HCL inputs via `.tf`, `.tfvars`, `.hcl`, and `terraform://...`.

```go
facts, _ := factsource.Load("gsheet://1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgVE2upms/Leads")
facts, _ = factsource.Load("postgres://arbiter:secret@db.internal/sales?table=facts&schema=governance")
facts, _ = factsource.Load("terraform:///srv/infra")
```

The same package can now write back to `.csv`, `.json`, `.jsonl`, `gsheet://...`, and `postgres://...` targets:

```go
_ = factsource.Save("gsheet://1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgVE2upms/Actions", facts)
_ = factsource.Save("postgres://arbiter:secret@db.internal/sales?table=facts&mode=replace", facts)
```

Sheets auth can come from `ARBITER_GSHEETS_API_KEY`, `ARBITER_GSHEETS_ACCESS_TOKEN`, or service-account JSON/file env vars. API keys work for read-only Sheets loads; writes require OAuth or a service account because the adapter clears stale rows before updating the target range.

Postgres targets require a `table=` query parameter and default to `schema=public`, `mode=replace`, and columns `type`, `key`, `fields`, and `version`. Loads return the row version on each fact, and writes run inside a serializable transaction. `mode=replace` is the authoritative full-snapshot path, while `mode=merge` upserts without deleting missing rows.

Terraform sources use gotreesitter's embedded HCL grammar directly. `.tf`, `.tfvars`, and `.hcl` files produce structured facts such as `Resource`, `Module`, `Variable`, `VariableValue`, and `Local`, with nested blocks and object/list values preserved as Go maps and slices. Resource blocks are exposed twice: once as generic `Resource` facts for cross-resource policies, and once under their concrete Terraform type such as `aws_s3_bucket` or `aws_instance` for narrow policies. `terraform://...` accepts a single file or a directory; `.json` targets are treated as `terraform show -json` plan output and extracted into both `Resource` and `ResourceChange` facts keyed by full Terraform address.

Chained arbiters now have a runtime surface too. The `workflow` package compiles the same `.arb` source once, creates one long-lived expert session per arbiter, topologically orders chain edges, and forwards only delta outcomes from upstream arbiters into downstream `source chain://...` inputs.

```go
w, _ := workflow.Compile(source, workflow.Options{})
_ = w.SetSourceFacts("https://transactions.internal/feed", []expert.Fact{{
    Type: "Transaction",
    Key:  "txn-1",
    Fields: map[string]any{
        "amount":  1500.0,
        "account": "acct-1",
    },
}})
result, _ := w.Run(context.Background())
_ = result.Arbiters["account_actions"].Delta.Outcomes
```

For reliable external I/O, `workflow.NewRunner` wraps the compiled graph with source polling and sink delivery behavior. It retries source loads with backoff, keeps last-known-good facts when a source is unavailable, exposes runtime health under `source.<alias>` and `sink.<alias>`, and can persist pending sink deliveries to a local JSONL journal for catch-up after a restart.

```go
runner, _ := workflow.NewRunner(w, workflow.RunnerOptions{
    DeliveryLog: "/var/lib/arbiter/deliveries.jsonl",
    Handlers: map[arbiter.ArbiterHandlerKind]workflow.OutcomeHandler{
        arbiter.ArbiterHandlerWebhook: workflow.OutcomeHandlerFunc(func(ctx context.Context, d workflow.Delivery) error {
            return deliverWebhook(ctx, d.Handler.Target, d.Outcome)
        }),
    },
})
tick, _ := runner.Tick(context.Background())
_ = tick.Sources["https://transactions.internal/feed"]
_ = tick.Sinks["webhook\x00https://hooks.internal/reviews"]
```

Rule-visible source metadata is derived from the runtime alias, so an external source like `https://feed.internal/facts` becomes `source.feed_internal_facts`. That gives the arbiter block enough information to distinguish fresh data from stale-but-usable data:

```arb
expert rule HaltOnStaleFeed priority 0 {
    when {
        source.feed_internal_facts.available == false
        and source.feed_internal_facts.__source_age_seconds > 300
    }
    then emit Halt {
        reason: "feed is stale for 5+ minutes",
    }
}
```

`workflow` still owns `chain://...` sources, validates that chain handlers and chain sources agree, and rejects cyclic arbiter graphs. Built-in delivery implementations cover `audit` and `stdout`; `webhook`, `slack`, `exec`, and `grpc` stay pluggable through `RunnerOptions.Handlers` so deployments can supply their own transport behavior without forking the runtime.

Arbiters are always killable by default. There is no `kill_switch` keyword inside an `arbiter` block because the loop should run unless a runtime stop path is used. The exact stop path can vary by deployment, but the invariant is the same: every arbiter must be stoppable quickly. In practice that can be wired through several control paths, including a control-plane override, a local override file, parent-context cancellation, or ordinary process shutdown/signal handling.

`CompileFull` still extracts these declarations alongside rules and segments. In the current codebase, the language surface plus `workflow` cover chained orchestration and reliable poll-driven runtime state, while streaming/scheduled trigger orchestration and fully built-in network transports remain one runtime layer above that.

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

Governed and flag traces use the same `check/result/detail` shape:

```json
[
  {
    "check": "requires BasicRiskCheck",
    "result": true,
    "detail": "BasicRiskCheck -> true"
  },
  {
    "check": "segment high_risk",
    "result": true,
    "detail": "model.risk_score > 0.8 -> true"
  },
  {
    "check": "rollout 20%",
    "result": false,
    "detail": "bucket(\"user_123\") = 57, threshold = 20"
  }
]
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

When the store is opened from a file, override mutations are persisted on every write.

## Serving

### gRPC API

Arbiter ships a gRPC server that bundles compilation, evaluation, flag resolution, expert sessions, runtime overrides, and audit logging into a single service.

```protobuf
service ArbiterService {
    rpc PublishBundle(...)       // compile and register .arb source
    rpc ListBundles(...)         // list bundle history and active versions
    rpc ActivateBundle(...)      // switch active version for a bundle name
    rpc RollbackBundle(...)      // move active version back one revision
    rpc GetBundle(...)           // fetch active source or immutable bundle by id
    rpc WatchBundles(...)        // stream bundle snapshots and live changes
    rpc GetOverrides(...)        // fetch runtime overrides for one bundle
    rpc WatchOverrides(...)      // stream override snapshot and live mutations
    rpc EvaluateRules(...)      // stateless rule evaluation
    rpc ResolveFlag(...)        // flag resolution with explainability
    rpc StartSession(...)       // create an expert session
    rpc RunSession(...)         // advance until quiescence / guardrail
    rpc AssertFacts(...)        // insert or update working-memory facts
    rpc RetractFacts(...)       // remove working-memory facts
    rpc GetSessionTrace(...)    // current facts, outcomes, activations
    rpc CloseSession(...)       // deterministically dispose of a live session
    rpc SetRuleOverride(...)    // runtime kill switch / rollout changes
    rpc SetFlagOverride(...)    // runtime flag kill switch
    rpc SetFlagRuleOverride(...)// runtime flag rule rollout changes
}
```

Bundles are published once and evaluated many times. Each bundle compiles rules, expert rules, flags, and segments from a single `.arb` source or from one root file expanded through `include`. Bundles now keep per-name history and an active version, so callers can evaluate by immutable `bundle_id` or by active `bundle_name`.

`GetBundle` returns the raw `.arb` source for one immutable `bundle_id` or the active bundle for one `bundle_name`. `WatchBundles` streams an initial snapshot plus `published`, `activated`, and `rolled_back` events so sidecars and local agents can keep a compiled local cache hot without polling.

`GetOverrides` returns the current runtime override set for one bundle, and `WatchOverrides` streams a typed snapshot followed by `rule`, `flag`, and `flag_rule` mutations keyed to immutable `bundle_id`.

### Audit

Every governance decision is written to a durable audit sink. The default `JSONLSink` appends one JSON object per line to a file. Implement the `audit.Sink` interface for your backend (database, event stream, object store).

```go
sink, _ := audit.NewJSONLSink("/var/log/arbiter/decisions.jsonl")
server := grpcserver.NewServer(registry, overrides, sink)
```

Each audit event captures the full context: matched rules, flag resolutions, expert session outcomes, governance trace steps, timestamps, request IDs, and bundle IDs.

Bundle publishes, activations, rollbacks, and override mutations are also emitted as audit events.

## Install

```bash
go install github.com/odvcencio/arbiter/cmd/arbiter@latest
```

## Editor Support

Tree-sitter consumers can use [highlights.scm](highlights.scm) directly for `.arb` highlighting. A minimal VS Code language package also ships in [editors/vscode/arbiter-language](editors/vscode/arbiter-language) with syntax highlighting, snippets, and `arbiter check` diagnostics on open/save.

## Usage

### CLI

```bash
arbiter compile rules.arb          # compile and show stats
arbiter eval rules.arb --data '{...}'  # evaluate against data
arbiter strategy rules.arb --name CheckoutRouting --data '{...}'
arbiter diff current.arb candidate.arb --data-file contexts.json --key request_id
arbiter replay candidate.arb --audit decisions.jsonl --request-id req-42
arbiter check rules.arb            # validate without emitting
arbiter expert tax.arb --envelope '{...}' [--facts '[...]']
arbiter serve --grpc :8081 --audit-file decisions.jsonl --bundle-file bundles.json --overrides-file overrides.json
arbiter-agent --upstream 127.0.0.1:8081 --grpc 127.0.0.1:7081 --status 127.0.0.1:7082 --bundle-name checkout --bundle-name pricing
```

`arbiter diff` answers “what changes if I ship this ruleset?” by evaluating two governed rulesets against the same JSON context or batch and reporting added, removed, and changed matches keyed by request context.

`arbiter replay` answers “what would happen now?” by reading audited `kind: "rules"` JSONL events, re-evaluating the recorded contexts, and reporting outcome drift. Use `--request-id` to focus on one audited decision or `--limit` to cap the batch.

`arbiter-agent` is the localhost data-plane form factor. It bootstraps one or many active bundles from the upstream control plane with `GetBundle`, keeps `WatchBundles(active_only=true)` streams open, syncs runtime overrides from `GetOverrides` plus `WatchOverrides`, and serves the normal Arbiter gRPC API from its own in-memory registry and override store.

Repeat `--bundle-name` to keep multiple bundles hot, or set `ARBITER_BUNDLE_NAMES=checkout,pricing`. The legacy single-value `ARBITER_BUNDLE_NAME` env var still works.

Set `--ready-max-staleness 30s` or `ARBITER_AGENT_READY_MAX_STALENESS=30s` if you want `/readyz` to fail once bundle or override sync freshness drifts beyond that age. `0s` keeps the old last-good behavior and disables freshness enforcement.

It also exposes local health and status on the HTTP listener:

- `GET /healthz` for process liveness
- `GET /readyz` for sync readiness, optionally gated by the configured freshness threshold
- `GET /status` for JSON introspection of synced bundles, checksums, bundle/override freshness, reconnect/error counters, and the last upstream failure when one is present

When `include` is involved, file-backed commands report diagnostics against the original source file:

```text
rules/segments.arb:14:1: rule EnterpriseDecision: rollout must be between 0 and 100
```

### Go Library — HTTP Middleware

You can embed governed rule evaluation directly into an existing `net/http` service. The middleware evaluates once per request, stores the result on the request context, and lets the next handler decide how to act on it.

```go
compiled, err := arbiter.CompileFullFile("rules.arb")
if err != nil {
	log.Fatal(err)
}

handler := arbiter.Middleware(compiled, func(r *http.Request) (map[string]any, error) {
	return map[string]any{
		"request": map[string]any{
			"method": r.Method,
		},
		"user": map[string]any{
			"role": r.Header.Get("X-Role"),
		},
	}, nil
}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	decision, ok := arbiter.DecisionFromRequest(r)
	if !ok {
		http.Error(w, "missing arbiter decision", http.StatusInternalServerError)
		return
	}
	for _, match := range decision.Matched {
		if match.Action == "Deny" {
			http.Error(w, "blocked by policy", http.StatusForbidden)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}))
```

If you do not want to hand-build the request context, pass `nil` and use `arbiter.DefaultHTTPContext`. It exposes request metadata under `request.method`, `request.path`, `request.host`, `request.headers`, and `request.query`. Header and query keys are normalized for `.arb` access, so `X-Debug` becomes `request.headers.x_debug` and `dry-run=true` becomes `request.query.dry_run == true`.

For stricter production behavior, use `arbiter.MiddlewareWithOptions` to supply custom request-context builders and custom handlers for context-build failures or evaluation failures.

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

Use file-aware APIs when your source uses `include`:

```go
ruleset, _ := arbiter.CompileFile("rules/main.arb")
full, _ := arbiter.CompileFullFile("rules/main.arb")
json, _ := arbiter.TranspileFile("rules/main.arb")
```

Continuous arbiters come back on the same compile path:

```go
full, _ := arbiter.CompileFull(source)
for _, decl := range full.Arbiters {
    fmt.Printf("%s killable=%t triggers=%d checkpoint=%q\n",
        decl.Name, decl.Killable, len(decl.Triggers), decl.Checkpoint)
}
```

### Go Library — Flags

```go
f, _ := flags.Load(source)
variant := f.Variant("checkout_v2", ctx)
eval := f.Explain("checkout_v2", ctx)
f, _ = flags.Watch("flags.arb")          // hot reload across the include graph
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

Long-lived sessions can sync authoritative source snapshots and inject a deterministic clock:

```go
session := expert.NewSession(program, envelope, nil, expert.Options{
    Now: func() time.Time { return fixedNow },
})

summary, _ := session.SyncFacts([]expert.Fact{
    {Type: "Lead", Key: "a", Fields: map[string]any{"score": 95.0}},
})
fmt.Printf("added=%d updated=%d retracted=%d\n", summary.Added, summary.Updated, summary.Retracted)
```

For multi-file expert programs:

```go
program, _ := expert.CompileFile("taxes/main.arb")
```

### Go Library — Authorization Helper

```go
decision, _ := authz.EvaluateSource(source, authz.Request{
    Actor: map[string]any{
        "role":   "admin",
        "org_id": "org_1",
    },
    Action: "read",
    Resource: map[string]any{
        "org_id": "org_1",
    },
})

if decision.Allowed {
    // one or more rules emitted Allow
}
```

The helper is intentionally thin. It just standardizes `actor`, `action`, and `resource` in the evaluation context and treats matched `Allow` actions as authorization success.

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

```arb
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

```arb
const VIP_THRESHOLD = 1000
const PREMIUM_TIERS = ["gold", "platinum"]
```

### Includes

Split one program across multiple files. Each included file is a valid `.arb` fragment.

```arb
include "schema.arb"
include "constants.arb"
include "segments.arb"
include "phase1_gross_income.arb"
include "phase2_deductions.arb"
include "phase3_agi.arb"
```

The compiler expands the include graph into one compilation unit. Constants, segments, rules, expert rules, and prerequisites work across file boundaries. `include` is file-based: use `CompileFile`, `CompileFullFile`, `TranspileFile`, `flags.LoadFile`, or `expert.CompileFile` when it is present.

### Rules

```arb
rule RuleName priority 1 {
    kill_switch                    # optional: instant disable
    requires OtherRule             # optional: prerequisite
    when segment high_value {      # optional: segment gate
        user.cart_total >= 100
    }
    then ActionName {
        type: "percentage",
        amount: 10,
    }
    otherwise FallbackAction {     # optional: when condition is false
        reason: "not eligible",
    }
    rollout 50                     # optional: percentage gate
}
```

### Expert Rules

```arb
expert rule RuleName priority 1 {
    kill_switch
    no_loop
    requires OtherRule
    activation_group Resolution
    when { income.wages > 0 }
    then assert GrossIncome {      # assert: mutate working memory
        key: "total",
        amount: income.wages + income.interest,
    }
    rollout 50
}

expert rule EmitResult priority 99 {
    when { any agi in facts.AGI { agi.amount > 0 } }
    then emit TaxReturn {          # emit: produce final outcome
        status: "complete",
    }
}

expert rule ClearFact {
    when { review.override == true }
    then retract RiskFlag {
        key: "account_123",
    }
}

expert rule UpdateFact {
    when { review.approved == true }
    then modify RiskFlag {
        key: "account_123"
        set {
            level: "low",
        }
    }
}
```

Expert rules also support binding syntax that compiles to nested existential quantifiers:

```arb
expert rule RouteManualReview {
    when {
        bind risk in facts.RiskFlag
        bind txn in facts.Transaction
        where {
            risk.account_id == txn.account_id
            and risk.level == "high"
        }
    }
    then emit ManualReview {
        queue: "risk",
    }
}
```

### Operators

**Comparison**

```text
x == 1          x != 1
x > 1           x < 1
x >= 1          x <= 1
```

**Logical**

```text
a and b         a or b          not a
```

**Collection**

```text
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

```text
name starts_with "Dr"
email ends_with ".edu"
code matches "^[A-Z]{3}$"
```

**Null**

```text
value is null
value is not null
```

**Range**

```text
age between [18, 65]            # inclusive both ends
score between (0, 100)          # exclusive both ends
temp between [0, 100)           # inclusive left, exclusive right
temp between (0, 100]           # exclusive left, inclusive right
```

**Math**

```text
price * quantity > 1000
score + bonus >= threshold
```

**Quantifiers**

```text
any item in cart.items { item.price > 100 }
all item in cart.items { item.in_stock == true }
none item in cart.items { item.banned == true }
```

**Grouping**

```text
(a > 1 or b > 2) and c > 3
```

## Architecture

```text
intern/       Constant pool — deduplicates strings and numbers across all rules
compiler/     CST → bytecode compiler + Arishem JSON loader
vm/           Stack-based bytecode VM (fixed 256-element stack, low-allocation eval)
govern/       Governance primitives: segments, rollouts, kill switches, prerequisites, trace
flags/        Feature flags: variants, schema validation, secret references, hot reload
strategy/     Native decision trees: exactly-one governed routing with trace
expert/       Forward-chaining inference: working memory, assert/emit/retract/modify, activation trace
audit/        Durable decision logging (Sink interface, JSONL default)
overrides/    Runtime governance overrides (kill switches, rollout percentages)
grpcserver/   gRPC service: bundle registry, evaluation, flag resolution, expert sessions
decompile/    Bytecode → Arishem JSON
sourceunit.go Multi-file include expansion for file-backed APIs
```

Flat `[]byte` of fixed-width 4-byte instructions: `[opcode(1B), flags(1B), arg(2B)]`. Constant pool indices are `uint16`, giving 65K unique values per type. The parser uses [gotreesitter](https://github.com/odvcencio/gotreesitter), and the repo now ships both a tree-sitter highlight query and a minimal VS Code language package for `.arb` files.

## Examples

### E-commerce Pricing

```arb
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

```arb
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

```arb
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

## Status

Arbiter is in **private alpha**. The language, compiler, VM, and inference engine are stable and well-tested. The gRPC API, sidecar agent, and workflow runtime are functional and deployed in production for internal use. The API surface may evolve as early adopters provide feedback.

What you can rely on today:

- Stateless rule evaluation at sub-microsecond latency
- Feature flag resolution with segments, rollouts, prerequisites, and kill switches
- Expert inference with forward-chaining, truth maintenance, and reversible overlays
- The `.arb` language syntax and governance keywords
- gRPC bundle management with versioning, activation, and rollback
- Audit trail on every decision
- Decision diff and replay via CLI

What is evolving:

- Continuous arbiter runtime (poll-based execution is stable; streaming and scheduled triggers are defined in the language but not yet orchestrated by the hosted runtime)
- Fact source and sink plugin ecosystem (CSV, JSON, JSONL, HTTP, Terraform, and Google Sheets are shipped; additional connectors are straightforward to add via the `Loader`/`Saver` interfaces)
- SDK coverage (Node, Python, and Rust gRPC stubs are generated; idiomatic wrapper libraries are not yet built)
- Multi-arbiter workflow chaining (functional and tested; production deployment patterns are still forming)

Arbiter is maintained by a solo author. Contributions, feedback, and design-partner conversations are welcome.

## License

Apache 2.0
