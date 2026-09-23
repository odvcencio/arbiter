# arbiter

## What's new in v1.10.0

**Prepared tag-filtered evaluation** — `vm.NewPreparedEvaluator` selects matching rule headers once for repeated decisions with the same rules, string pool, and tags.

**Existing evaluation semantics** — prepared evaluation keeps rule order, active windows, actions, errors, and outputs consistent with `EvalWithTagFilter`.

---

A compact language for governed outcomes.

Every decision your software makes — approve this transaction, show this variant, block this request, compute this tax bracket — is a governed outcome. Arbiter gives those decisions a language, compiles them to bytecode, and evaluates simple precompiled rules in the low hundreds of nanoseconds.

```text
.arb source ──→ Parser ──→ Compiler ──→ Bytecode VM (~200ns simple eval)
                  │                         │
                  └──→ Module imports        └──→ Binary bundle (edge/mobile)
```

Four modalities, one language. **Stateless evaluation** for request-path decisions. **Feature flags** for governed variant resolution. **Expert inference** for forward-chaining reasoning until quiescence. **Continuous arbiters** for always-on decision loops. Same compiler, same VM, same governance.

## Agent Skill

Agents helping someone use Arbiter as a dependency should read the canonical M31 Labs skill: [using-arbiter](https://github.com/odvcencio/m31labs-skills/blob/main/skills/using-arbiter/SKILL.md).

Choose the lightest sufficient mode:

- `rule` = many matching governed outcomes
- `strategy` = exactly-one governed route
- `flag` = governed variant resolution
- `expert` = fact mutation until quiescence
- `arbiter` = long-lived loop over sources and outcomes

The parser is built on [gotreesitter](https://github.com/odvcencio/gotreesitter), a pure-Go reimplementation of the tree-sitter runtime — no CGo, no C toolchain, no generated files. Cross-compiles to any `GOOS`/`GOARCH` target Go supports, including WASM.

Standalone reference material lives under [`docs/`](docs):

- [`docs/language/grammar.ebnf`](docs/language/grammar.ebnf) is the tooling-facing language specification.
- [`docs/self-hosted.md`](docs/self-hosted.md) is the recommended self-hosted profile for a single team or trust boundary.

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
ARBITER_BENCH_IN_CLUSTER_ADDR=arbiter.default.svc.cluster.local:8081 \
go test -run '^$' -bench '^BenchmarkLatencySplit/grpc_in_cluster$' -benchmem -benchtime=100x
```

The gRPC benches publish the bundle once, warm it up, and benchmark `EvaluateRules` only.

## Governance

Segments, rollouts, kill switches, prerequisites, explainability — governance primitives that apply to any outcome. Rules, strategies, flags, and expert inferences all share them.

Within stateless governed evaluation, rules collect applicable outcomes, strategies select one ordered path, and flags resolve named variants.

For maximum readability, use one house style across the repo:

- Start each module with typed declarations: `input`, `feature`, `fact`, `outcome`, `table`
- Follow with shared governance and reuse points: `const`, `tag`, `segment`
- End with one dominant decision surface: `rule`, `strategy`, `flag`, `expert rule`, or `arbiter`
- Prefer one business question per file. If a file starts answering two different questions, split it.
- Split by business domain first, then by modality only when one file stops fitting on one screen
- Keep workers and arbiters in runtime-facing modules; keep typed declarations and reusable segments in shared modules
- Keep imports flowing inward: shared schemas and segments feed rules, flags, strategies, and experts; runtime-facing arbiters and workers sit at the edge
- Put the `.test.arb` file next to the module it explains so behavior and specification move together
- Treat more than one owner, more than one rollout surface, or more than one screen of governed declarations as a split trigger

Across modalities, keep clause order predictable:

- Governance prelude first: `kill_switch`, `active_from` / `active_until`, modality-specific prereq or stability clauses, then `rollout`
- Matching or binding second: `when`, `segment`, candidate conditions, or fact bindings
- Effect last: `then`, `otherwise`, `assert`, `emit`, or runtime routing
- If a clause does not apply to a modality, skip it; do not reorder the rest

One clean project layout looks like:

```text
arbiter.toml
shared/input.arb
shared/outcomes.arb
shared/segments/risk.arb
payments/rules.arb
checkout/strategy.arb
checkout/strategy.test.arb
experiments/flags.arb
tax/expert.arb
runtime/workers/notify.arb
runtime/arbiters/fraud_monitor.arb
```

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
    kill_switch on
    active_from 2026-01-01T00:00:00Z
    active_until 2026-02-01T00:00:00Z
    requires BasicRiskCheck
    rollout 20
    when segment high_risk {
        tx.amount > 5000
    }
    then Flag { level: "hold" }
}
```

#### Segment-only rule form

When the segment gate is the entire condition — no additional inline expression needed — omit the `{ ... }` block entirely:

```arb
segment vip_tier {
    user.lifetime_spend > 10000
}

rule VIPBenefit {
    when segment vip_tier
    then ApplyBenefit {
        type: "free_shipping",
    }
}
```

Semantics: an absent (or empty) condition means **unconditional** — the segment gate alone decides whether the rule fires. This is distinct from `when segment NAME { expr }`, which ANDs the segment with an inline predicate. Prior to v1.8.1, the no-brace form was a parse error; on some older eval paths a rule with no condition silently never fired instead of firing unconditionally.

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

Across governed declarations, the canonical shape is: governance prelude, then matching/binding, then effect.

```arb
outcome CheckoutPath {
    target: string
    reason: string
}

strategy CheckoutRouting returns CheckoutPath {
    kill_switch off
    active_from 2026-01-01T00:00:00Z
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

They reuse the same conditions, segments, rollouts, and arbitrace machinery as rules, but the evaluation model recognizes one named shape and then takes the first matching governed path with an explicit fallback.

`active_from` is inclusive. `active_until` is exclusive. The same structural window works on governed rules, strategy candidates, and flag rules, and uses `__now` if provided in the request context; otherwise Arbiter uses the current UTC time.

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

    active_from 2026-01-01T00:00:00Z
    when beta_users then "treatment"
    rollout 50 when { user.country == "US" } then "treatment"
}
```

Schema validation, secret references, request-level caching, hot reload, HTTP serving, explainability arbitraces — all come along.

### Expert Inference

Forward-chaining rules that reason until quiescence. Facts build on facts. Rules fire, assert new facts into working memory, and the engine loops until nothing changes.

```arb
expert rule ComputeAGI priority 15 {
    requires ComputeGrossIncome
    rollout 50
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

The session runs with guardrails — configurable max rounds and max mutations, context cancellation. Every firing is recorded in the activation arbitrace.

`modify` and `retract` are reversible overlays, not one-way destructive writes. If the supporting rule stops matching, the underlying fact view is recomputed and the overlay falls away. That can produce a steady-state no-op activation in the arbitrace while a modifier or retractor remains active.

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

Workers fit beside arbiters as named typed capabilities, not as a second decision modality. An arbiter still owns triggers, working memory, governance, and routing. A worker owns a typed input, a typed output, and one runtime transport.

```arb
outcome RiskAlert {
    key: string
    severity: string
}

fact ExecutionResult {
    status: string
}

worker kill_all_orders {
    input RiskAlert
    output ExecutionResult
    exec "kill-all-orders"
}

arbiter trading_system {
    poll 5s
    source https://exchange.internal/risk
    source worker://kill_all_orders

    on RiskAlert where severity == "critical" worker kill_all_orders
}
```

`source worker://name` is runtime-owned: the runner materializes successful worker results there on the next tick so expert rules can reason about them without turning worker execution into an imperative in-tick loop.

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

For reliable external I/O, `workflow.NewRunner` wraps the compiled graph with source polling and sink delivery behavior. It retries source loads with backoff, keeps last-known-good facts when a source is unavailable, exposes runtime health under `source.<alias>` and `sink.<alias>`, can persist pending sink deliveries to a local JSONL journal for catch-up after a restart, and can fan out independent source polls and handler targets with bounded concurrency.

```go
runner, _ := workflow.NewRunner(w, workflow.RunnerOptions{
    DeliveryLog:              "/var/lib/arbiter/deliveries.jsonl",
    MaxConcurrentSourceLoads: 8,
    MaxConcurrentDeliveries:  8,
    Handlers: map[arbiter.ArbiterHandlerKind]workflow.OutcomeHandler{
        arbiter.ArbiterHandlerWebhook: workflow.OutcomeHandlerFunc(func(ctx context.Context, d workflow.Delivery) error {
            return deliverWebhook(ctx, d.Handler.Target, d.Outcome)
        }),
    },
    WorkerHandlers: map[arbiter.ArbiterHandlerKind]workflow.WorkerHandler{
        arbiter.ArbiterHandlerExec: workflow.WorkerHandlerFunc(func(ctx context.Context, invocation workflow.WorkerInvocation) (workflow.WorkerExecution, error) {
            err := runKillAllOrders(ctx, invocation.Delivery.Outcome)
            if err != nil {
                return workflow.WorkerExecution{}, err
            }
            return workflow.WorkerExecution{
                Facts: []expert.Fact{{
                    Type: "ExecutionResult",
                    Key:  invocation.Delivery.Outcome.Params["key"].(string),
                    Fields: map[string]any{
                        "status": "submitted",
                    },
                }},
            }, nil
        }),
    },
})
tick, _ := runner.Tick(context.Background())
_ = tick.Sources["https://transactions.internal/feed"]
_ = tick.Sinks["webhook\x00https://hooks.internal/reviews"]
```

`MaxConcurrentSourceLoads` parallelizes external fetches for different declared sources in the same tick. `MaxConcurrentDeliveries` drains independent handler targets concurrently while still preserving delivery order for the same target or worker, so one webhook/audit file/worker stays serialized and different targets can make forward progress at once.

The current scaling model is straightforward: stateless eval, flags, and strategies scale horizontally by request fanout; expert sessions stay sticky to one instance per session; continuous arbiters scale up inside one runner through bounded source/delivery parallelism and scale out by sharding bundles or arbiter graphs across runner instances.

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

`workflow` still owns `chain://...` and `worker://...` sources, validates that chain handlers and worker sources point at declared runtime objects, and rejects cyclic arbiter graphs. `chain` and `worker` remain reserved handler kinds, and `stdout` remains the only targetless runtime kind. Everything else is host-owned capability space: Go hosts can register sink and worker kinds directly through `RunnerOptions.Handlers` and `RunnerOptions.WorkerHandlers`, and non-Go runtimes can expose the gRPC `CapabilityService` and bind it through `capability.NewGRPCAdapter` or `arbiter-runtime --capability-grpc ...`. Built-in delivery implementations still cover `audit` and `stdout`, while transport kinds like `webhook`, `slack`, `exec`, `grpc`, or your own identifiers stay deployment-defined.

Keep the transport algebra explicit: a sink kind is not automatically a worker runtime. Workers are typed capabilities with typed results, so their runtime kinds must be registered through `RunnerOptions.WorkerHandlers` (or the capability plugin's worker surface), not inferred from delivery handlers.

That same capability surface is now visible to operators too: `/status` reports one unified runtime view of source schemes, sink kinds, and worker runtimes tagged by owner (`core`, `host`, or `plugin`), includes the connected plugin manifest metadata when a sidecar is present, and now exposes the runtime's control-surface auth/TLS posture plus capability-plugin transport settings directly instead of forcing operators to infer them from flags.

Arbiters are always killable by default. There is no `kill_switch` keyword inside an `arbiter` block because the loop should run unless a runtime stop path is used. The exact stop path can vary by deployment, but the invariant is the same: every arbiter must be stoppable quickly. In practice that can be wired through several control paths, including a control-plane override, a local override file, parent-context cancellation, or ordinary process shutdown/signal handling.

`CompileFull` still extracts these declarations alongside rules and segments. In the current codebase, the language surface plus `workflow` cover chained orchestration and reliable poll-driven runtime state, while streaming/scheduled trigger orchestration and fully built-in network transports remain one runtime layer above that.

### Explainability

Every evaluation produces an inspectable decision arbitrace. Rules, flags, strategies, and expert sessions now share the same arbitrace nouns: `phase`, `scope`, `subject`, `kind`, `check`, `result`, `detail`, `disposition`.

```go
// Stateless rules
matched, arbitrace, _ := arbiter.EvalGoverned(ruleset, dc, segments, ctx)

// Flags
eval := flags.Explain("checkout_v2", ctx)

// Strategies
result, _ := strategies.Evaluate("CheckoutRouting", ctx)

// Expert inference
result, _ := session.Run(ctx)
result.Activations // every firing, every round, what changed, and why it was allowed
```

The canonical wire and JSON field name is now `arbitrace`, and the shared protobuf step type is `ArbitraceStep`.

`result` remains the legacy bool surface for compatibility. `disposition` is the canonical Arbiter outcome for a step: `passed`, `blocked`, or `deferred`. Conservative deferral therefore stays visible instead of collapsing into a generic false.

Stateless temporal eligibility is first-class too: `active_from` and `active_until` record their own governance steps instead of disappearing into generic conditions.

Rules, flags, and strategies preserve the legacy `check/result/detail` shape for compatibility, but also carry structured semantics:

```json
[
  {
    "check": "requires BasicRiskCheck",
    "phase": "governance",
    "scope": "rule",
    "subject": "EnhancedRiskCheck",
    "kind": "requires",
    "target": "BasicRiskCheck",
    "result": true,
    "disposition": "passed",
    "detail": "BasicRiskCheck -> true"
  },
  {
    "check": "segment high_risk",
    "phase": "match",
    "scope": "rule",
    "subject": "EnhancedRiskCheck",
    "kind": "segment",
    "target": "high_risk",
    "result": true,
    "disposition": "passed",
    "detail": "model.risk_score > 0.8 -> true"
  },
  {
    "check": "rollout percent 20 by user.id namespace \"bundle:rule:EnhancedRiskCheck\"",
    "phase": "governance",
    "scope": "rule",
    "subject": "EnhancedRiskCheck",
    "kind": "rollout",
    "result": false,
    "disposition": "blocked",
    "detail": "subject_key=user.id, subject=\"user_123\", namespace=\"bundle:rule:EnhancedRiskCheck\", bucket=5700, threshold=2000, resolution=10000"
  }
]
```

Expert activations now carry the same arbitrace structure per firing, so a session snapshot tells you both what mutated and which governance/match checks made that mutation eligible.

Expert session snapshots and `RunSession` responses also expose `stable_deferred` and `temporal_pending`, so callers can tell whether the engine is still conservatively waiting on quiescence or temporal maturation when a run stops early.

That same `ArbitraceStep` shape now flows through gRPC responses too: `EvaluateRules`, `ResolveFlag`, `EvaluateStrategy`, `RunSession`, and `GetSessionArbitrace` all expose the structured fields instead of flattening back to `check/result/detail` only.

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

Override inspection surfaces now expose explicit kill-switch mode as well as the legacy bools: snapshots, watch events, and audit records carry `kill_switch_state` (`on`, `off`, or unset) so UIs do not need to reconstruct state from `kill_switch_set` plus `kill_switch`.

## Serving

### gRPC API

Arbiter ships a gRPC server with compilation, evaluation, flag resolution, expert sessions, runtime overrides, audit logging, and full observability (Prometheus metrics, structured logging via `slog`, OpenTelemetry trace propagation).

The server exposes two listeners: gRPC for API traffic, HTTP for `/metrics`, `/healthz`, `/readyz`, `/status`.

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
    rpc GetSessionArbitrace(...)    // current facts, outcomes, activations
    rpc CloseSession(...)       // deterministically dispose of a live session
    rpc SetRuleOverride(...)    // runtime kill switch / rollout changes
    rpc SetFlagOverride(...)    // runtime flag kill switch
    rpc SetFlagRuleOverride(...)// runtime flag rule rollout changes
}

service ControlService {
    rpc GetControlStatus(...)   // hosted control-plane posture and state
}
```

Bundles are published once and evaluated many times. Each bundle compiles rules, expert rules, flags, and segments from a single `.arb` source or from one root file expanded through `include`. Bundles now keep per-name history and an active version, so callers can evaluate by immutable `bundle_id` or by active `bundle_name`.

`GetBundle` returns the raw `.arb` source for one immutable `bundle_id` or the active bundle for one `bundle_name`. `WatchBundles` streams an initial snapshot plus `published`, `activated`, and `rolled_back` events so sidecars and local agents can keep a compiled local cache hot without polling.

`GetOverrides` returns the current runtime override set for one bundle, and `WatchOverrides` streams a typed snapshot followed by `rule`, `flag`, and `flag_rule` mutations keyed to immutable `bundle_id`. Override entries preserve the compatibility bool fields and also expose canonical `kill_switch_state` so operator tooling can reason about override intent directly.

`arbiter serve` and `arbiter-runtime --grpc` now share the same in-process hardening shape:

- `--auth-token` / `--auth-token-file` for bearer-token auth
- `--tls-cert`, `--tls-key`, and optional `--tls-client-ca` for TLS or mTLS
- `--max-recv-bytes` / `--max-send-bytes` to bound gRPC message sizes
- `--rate-limit-rpm` / `--rate-limit-burst` for per-caller token-bucket limits
- `--session-ttl`, `--session-max`, and `--session-max-per-owner` to constrain expert-session state
- `--data-dir` or explicit `--bundle-file` / `--overrides-file` for file-backed persistence, or `--ephemeral` for memory-only mode

The hosted control plane now exposes the same kind of operator surface as the runtime and agent. `ControlService.GetControlStatus` and HTTP `/status` report `readiness -> issues -> transport -> bundles -> overrides -> sessions -> audit`, including listener auth/TLS posture, whether bundle and override persistence are actually healthy, active bundle versions, live expert-session occupancy, and whether decision recording is durable, healthy, and currently succeeding. HTTP `/readyz` now follows that same readiness model instead of reporting green while configured durable surfaces are failing.

### Audit

Every governance decision is written to a durable audit sink. The default `JSONLSink` appends one JSON object per line to a file. Implement the `audit.Sink` interface for your backend (database, event stream, object store).

```go
sink, _ := audit.NewJSONLSink("/var/log/arbiter/decisions.jsonl")
server := grpcserver.NewServer(registry, overrides, sink)
```

Each audit event captures the full context: matched rules, flag resolutions, expert session outcomes, governance arbitrace steps, timestamps, request IDs, and bundle IDs. Override mutations also preserve explicit `kill_switch_state`, and expert activations include their per-firing arbitrace in the audited payload.

Bundle publishes, activations, rollbacks, and override mutations are also emitted as audit events.

## Install

```bash
go install github.com/odvcencio/arbiter/cmd/arbiter@latest
```

## Editor Support

The Arbiter LSP (`arbiter-lsp`) provides:

- **Diagnostics** — compile errors with source locations, including cross-module import errors
- **Completions** — facts, outcomes, segments, strategies, rules, keywords with context
- **Hover** — schema fields, rule summaries with priorities
- **Go-to-definition** — jump to any declaration across files
- **Find references** — all usage sites for any declaration
- **Rename** — whole-word rename with word-boundary checking
- **Document symbols** — outline view of all declarations
- **Formatting** — canonical formatting including table column alignment
- **Semantic highlighting** — color-codes fact/outcome names, table names, member access, and module prefixes
- **Code actions** — add missing outcome fields, add `else` to lookup, add `requires`, import quick fix
- **Multi-file diagnostics** — errors in imported modules surface in both files

The VS Code extension ships in [editors/vscode/arbiter-language](editors/vscode/arbiter-language) with format-on-save enabled by default. Tree-sitter consumers can use [highlights.scm](highlights.scm) directly.

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
arbiter runtime-capabilities grpcs://runtime.internal:7443 --token "$ARBITER_RUNTIME_TOKEN" --ca-file /etc/arbiter/runtime-ca.pem --json
arbiter runtime-status grpcs://runtime.internal:7443 --token "$ARBITER_RUNTIME_TOKEN" --ca-file /etc/arbiter/runtime-ca.pem
arbiter agent-status grpcs://127.0.0.1:7081 --token "$ARBITER_AGENT_TOKEN"
arbiter control-status grpcs://arbiter.internal:7443 --token "$ARBITER_TOKEN" --ca-file /etc/arbiter/control-ca.pem
arbiter serve --grpc 127.0.0.1:8081 --status 127.0.0.1:8082 --auth-token "$ARBITER_TOKEN" --max-recv-bytes 4194304 --data-dir ./state
arbiter-agent --upstream https://arbiter.internal:443 --upstream-token "$ARBITER_TOKEN" --bundle-name checkout --grpc 127.0.0.1:7081 --status 127.0.0.1:7082
```

`arbiter diff` answers “what changes if I ship this ruleset?” by evaluating two governed rulesets against the same JSON context or batch and reporting added, removed, and changed matches keyed by request context.

`arbiter replay` answers “what would happen now?” by reading audited `kind: "rules"` JSONL events, re-evaluating the recorded contexts, and reporting outcome drift. Use `--request-id` to focus on one audited decision or `--limit` to cap the batch.

`arbiter-agent` is the localhost data-plane form factor. It bootstraps one or many active bundles from the upstream control plane with `GetBundle`, keeps `WatchBundles(active_only=true)` streams open, syncs runtime overrides from `GetOverrides` plus `WatchOverrides`, and serves the normal Arbiter gRPC API from its own in-memory registry and override store. Its `/status` surface now follows the same inspection pattern as the runtime: `readiness`, `issues`, `transport`, and `sync` sections up front, then the legacy flat counters and bundle snapshots behind them for compatibility. The same canonical shape is available over gRPC through `AgentService.GetAgentStatus`.

Repeat `--bundle-name` to keep multiple bundles hot, or set `ARBITER_BUNDLE_NAMES=checkout,pricing`. The legacy single-value `ARBITER_BUNDLE_NAME` env var still works.

Set `--ready-max-staleness 30s` or `ARBITER_AGENT_READY_MAX_STALENESS=30s` if you want `/readyz` to fail once bundle or override sync freshness drifts beyond that age. `0s` keeps the old last-good behavior and disables freshness enforcement.

Use `--upstream-token`, `--upstream-ca-file`, `--upstream-server-name`, or `--upstream-plaintext` when the upstream control plane is protected with auth and TLS.

Use `--auth-token`, `--auth-token-file`, `--tls-cert`, `--tls-key`, and optional `--tls-client-ca` when the agent's local gRPC surface leaves localhost or crosses a trust boundary.

`arbiter runtime-capabilities`, `arbiter runtime-status`, `arbiter agent-status`, and `arbiter control-status` accept `grpc://`, `http://`, `grpcs://`, `https://`, or a bare `host:port`. Use `--token`, `--ca-file`, and `--server-name` for secure control-surface access, or `--plaintext` to force insecure transport against a bare target. The inspection commands now split cleanly: `runtime-capabilities` reports transport plus capability algebra, `runtime-status` reports `operator -> readiness -> issues -> transport -> capabilities -> activity`, `agent-status` reports `operator -> readiness -> issues -> transport -> sync`, and `control-status` reports `operator -> readiness -> issues -> transport -> bundles -> overrides -> sessions -> audit`, with a canonical issue list for concrete failures and insecure public/plaintext transport posture alongside bundle/override persistence health plus audit write/error counters and the last successful or failed persistence or audit write times.

Add `--fail-on-issues` to `runtime-status`, `agent-status`, or `control-status` when you want scripts or CI to exit non-zero on blocking issues instead of only printing them.

Issue codes are treated as part of the operator contract. Use `arbiter status-issues` for the local catalog, `arbiter status-issues grpcs://host:port --surface runtime|agent|control` when you want the live remote catalog through the CLI, `GetStatusIssueCatalog` from runtime, agent, or control gRPC clients for the RPC form, or `/status/issues` on the self-hosted HTTP status surfaces when you want the scoped JSON catalog directly from a running process. Every status and catalog surface now advertises `operator.product`, `operator.build_version`, and `operator.operator_contract_version` up front so live tooling can see which build and operator contract it is actually talking to. See [docs/status-issues.md](docs/status-issues.md) for the canonical vocabulary and blocking semantics.

### Self-Hosted Profile

The credible self-hosted shape today is one Arbiter deployment per trust boundary, backed by persistent disk and protected with bearer auth plus TLS at the edge or directly in-process. Stateless rules, flags, strategies, and continuous runtimes scale cleanly behind a load balancer. Expert sessions do not migrate between replicas, so run them on one instance or use sticky routing when that mode is active.

See [`docs/self-hosted.md`](docs/self-hosted.md) for the recommended operating profile and [`deploy/k8s.yaml`](deploy/k8s.yaml) for the reference Kubernetes manifest.

It also exposes local health and status on the HTTP listener:

- `GET /healthz` for process liveness
- `GET /readyz` for sync readiness, optionally gated by the configured freshness threshold
- `GET /status` for JSON introspection of synced bundles, checksums, bundle/override freshness, reconnect/error counters, watch connectivity, and a canonical `readiness -> issues -> transport -> sync` section layout up front

When `include` is involved, file-backed commands report diagnostics against the original source file:

```text
rules/segments.arb:14:1: rule EnterpriseDecision: rollout must be between 0 and 100
```

### Go Library — HTTP Middleware

You can embed governed rule evaluation directly into an existing `net/http` service. The middleware evaluates once per request, stores the result on the request context, and lets the next handler decide how to act on it.

```go
compiled, err := arbiter.CompileFile("rules.arb")
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
prog, _ := arbiter.Compile(source)
dc := arbiter.DataFromMap(data, prog)

// Fast path — no governance
matched, _ := arbiter.Eval(prog, dc)

// Governed path — segments, kill switches, rollouts, prerequisites, arbitrace
matched, arbitrace, _ := arbiter.EvalGoverned(prog, dc, prog.Segments, ctx)

// Selective evaluation — filter by tags
matched, _ = arbiter.Eval(prog, dc, arbiter.WithTags("fraud"))
```

Use file-aware APIs when your source uses `import` or `include`:

```go
prog, _ := arbiter.CompileFile("rules/main.arb")
prog, _ = arbiter.CompileFile("rules/main.arb", arbiter.WithManifest("arbiter.toml"))
```

`Compile` returns a unified `*Program` with `Ruleset`, `Segments`, `Strategies`, `Input`, `IR`, and `Warnings`.

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
src, _ := arbiter.ConvertJSONRules([]arbiter.JSONRule{{name, priority, condJSON, actJSON}})
prog, _ := arbiter.Compile(src)
dc, _ := arbiter.DataFromJSON(inputJSON, prog)
matched, _ := arbiter.Eval(prog, dc)
```

## Language

### Typed Data Declarations

`input`, `feature`, `fact`, `outcome`, and `table` are one family: declared data shapes Arbiter can validate, inspect, and reason about. Use `input` for request shape, `feature` for sourced data, `fact` for working memory, `outcome` for governed effects, and `table` for immutable lookup data.

### Features

Declare sourced data your rules evaluate against.

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

### Modules

Split programs across files with namespaced imports. An `arbiter.toml` manifest at the project root anchors import resolution.

```arb
import "fraud/scoring"
import "fraud/scoring" as fs
```

Imported declarations are accessed via namespace: `requires scoring.BaseRule`, `segment scoring.HighRisk`. All top-level declarations are visible — no export gating. Circular imports are a compile error.

```toml
# arbiter.toml
[project]
name = "acme-fraud"
version = "1.5.0"
```

### Input Schemas

Declare expected input shape for compile-time path validation.

```arb
input {
    user: {
        id: string
        age: number
        balance: decimal<USD>
    }
    request: {
        amount: decimal<USD>
        tags: list<string>
    }
}
```

Unknown paths and type mismatches are compile errors. Optional when absent — v1.0 runtime behavior unchanged.

### Tables

Named immutable data for lookup-driven decisions. Replaces combinatorial rule explosion.

```arb
table h264_ladder {
    height: number | video_bitrate: string | audio_bitrate: string | preset: string
    1080           | "6500k"               | "160k"              | "p3"
    720            | "3800k"               | "128k"              | "p3"
    480            | "1200k"               | "96k"               | "p2"
}

rule Transcode {
    when { job.codec == "h264" }
    then Profile {
        let row = lookup h264_ladder
            where height <= job.target_height
            order by height desc
            else { height: 0, video_bitrate: "800k", audio_bitrate: "96k", preset: "p2" }
        video_bitrate: row.video_bitrate,
        audio_bitrate: row.audio_bitrate,
    }
}
```

### Tags

Organize rules into groups for selective evaluation.

```arb
tag "fraud"
tags "realtime,batch"

rule HighValue tag "fraud" tag "realtime" {
    when { transaction.amount > 5000 USD }
    then Flag { reason: "high value" }
}
```

Evaluate subsets: `arbiter.Eval(prog, dc, arbiter.WithTags("fraud"))`. Tags are a closed set — undeclared tags are compile errors.

### Includes (Deprecated)

`include` still works but emits a deprecation warning. Use `import` for new code.

```arb
include "schema.arb"
include "segments.arb"
```

### Rules

```arb
rule RuleName priority 1 {
    kill_switch on                 # optional: instant disable ("off" is explicit no-op)
    requires OtherRule             # optional: prerequisite
    rollout 50                     # optional: percentage gate
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
}
```

### Expert Rules

```arb
expert rule RuleName priority 1 {
    kill_switch on
    no_loop
    requires OtherRule
    activation_group Resolution
    rollout 50
    when { income.wages > 0 }
    then assert GrossIncome {      # assert: mutate working memory
        key: "total",
        amount: income.wages + income.interest,
    }
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

## Continuous Arbiters

Continuous arbiters are always-on decision loops declared in the language. They process event streams, load facts from external sources, run expert inference, and route outcomes to handlers.

### Single Arbiter

```arb
fact Transaction {
    user: string
    amount: decimal<currency>
    country: string
}

outcome FraudAlert {
    user: string
    reason: string
    severity: string
}

expert rule VelocityDetection priority 10 {
    when {
        any tx in facts.Transaction { tx.amount > 0 USD }
    }
    then emit FraudAlert {
        user: tx.user,
        reason: "velocity",
        severity: "medium",
    }
}

arbiter fraud_monitor {
    stream transaction         # trigger: subscribe to transaction events
    source transaction         # fact source: materialize events as facts
    source user_profile        # fact source: load user profiles

    on FraudAlert where severity == "high" slack #fraud-alerts
    on FraudAlert audit /var/log/fraud.jsonl
    on * stdout
}
```

The `arbiter` declaration wires together:
- **Triggers** — `poll` (interval), `stream` (subscription), `schedule` (cron)
- **Sources** — fact providers loaded before each evaluation
- **Handlers** — route outcomes to reserved runtime kinds (`chain`, `worker`, `audit`, `stdout`) or any host-registered sink kind such as `webhook`, `slack`, `exec`, `grpc`, or `discord`

### Chained Arbiters

Outcomes from one arbiter become facts in another via `chain`. The workflow engine executes them in topological order.

```arb
arbiter fraud_detector {
    stream transaction
    source transaction

    on FraudAlert chain risk_scorer    # forward alerts to next stage
    on * stdout
}

arbiter risk_scorer {
    poll 1s
    source chain://fraud_detector      # receive chained facts

    on RiskAssessment chain response_handler
    on * stdout
}

arbiter response_handler {
    poll 1s
    source chain://risk_scorer

    on BlockUser audit /var/log/blocks.jsonl
    on * stdout
}
```

The `workflow/` package handles chaining locally: `workflow.Compile()` builds the graph, validates for cycles, topologically sorts the arbiters, and `workflow.Run()` executes one pass with outcome→fact propagation between stages.

### Testing Continuous Arbiters

`.test.arb` files test continuous arbiter scenarios with `stream` events and `within` time windows:

```arb
scenario "velocity detection triggers on transactions" {
    stream transaction { key: "tx-1", user: "alice", amount: 100 USD, country: "US" }
    stream transaction { key: "tx-2", user: "alice", amount: 200 USD, country: "US" }

    within 1m {
        expect outcome FraudAlert { user: "alice", reason: "velocity" }
    }
}
```

## Reference Runtime

`arbiter-runtime` is the canonical host process for continuous arbiters and workers:

```bash
arbiter-runtime \
  --bundle monitor.arb \
  --grpc 127.0.0.1:7081 \
  --auth-token "$ARBITER_RUNTIME_TOKEN" \
  --capability-grpc grpcs://127.0.0.1:7090 \
  --capability-token "$ARBITER_CAPABILITY_TOKEN" \
  --capability-ca-file /etc/arbiter/capability-ca.pem \
  --poll 5s \
  --status :7082 \
  --source-parallelism 8 \
  --delivery-parallelism 8
```

It handles the full lifecycle:
- **Arbiter loop** — ticks on the declared poll interval, runs all arbiters in topological order
- **Source polling** — loads external fact sources with retry and exponential backoff; keeps last-known-good facts on failure
- **Capability plugins** — optional gRPC sidecars can register source schemes plus sink and worker kinds without embedding Go
- **Worker dispatch** — executes registered worker runtimes and materializes results as `worker://` source facts
- **Delivery retry** — outcomes route to registered handlers with durable retry journal
- **Bounded parallelism** — independent sources and handler targets can run concurrently inside one tick without changing per-target ordering
- **Chain propagation** — outcomes from upstream arbiters become facts in downstream arbiters
- **Health endpoints** — `/healthz` (liveness), `/readyz` (first tick completed), `/status` (JSON sections: `readiness`, `issues`, `transport`, `capabilities`, `activity`, plus legacy flat mirrors for compatibility)
- **Runtime control RPC** — optional `RuntimeService.GetRuntimeCapabilities` and `RuntimeService.GetRuntimeStatus` expose the runtime's canonical capability surface plus its `readiness -> issues -> transport -> capabilities -> activity` status surface over gRPC for SDKs and CLI clients
- **Agent control RPC** — optional `AgentService.GetAgentStatus` exposes the agent's canonical `readiness -> issues -> transport -> sync` surface over the same local gRPC listener
- **Hosted control RPC** — `ControlService.GetControlStatus` exposes the hosted control plane's canonical `readiness -> issues -> transport -> bundles -> overrides -> sessions -> audit` surface over the same gRPC listener as bundle lifecycle and evaluation APIs, including live bundle/override persistence health plus audit health and last-error state. The paired HTTP `/readyz` now degrades when those configured durable surfaces are unhealthy.

Runtime transport is now opinionated instead of ad hoc:

- Runtime control RPC uses the same `--auth-token`, `--auth-token-file`, `--tls-cert`, `--tls-key`, and `--tls-client-ca` hardening model as `arbiter serve`
- Capability plugins use the same target grammar as the rest of the product: `grpc://`, `grpcs://`, `http://`, `https://`, or bare `host:port`
- Use `--capability-token`, `--capability-ca-file`, `--capability-server-name`, or `--capability-plaintext` to make plugin transport explicit instead of ambient

Build:

```bash
go build -tags grammar_blobs_external -o arbiter-runtime ./cmd/arbiter-runtime
```

## Architecture

```text
intern/        Constant pool — deduplicates strings and numbers across all rules
compiler/      CST → IR → bytecode compiler (with constant folding), table compilation, regex pre-compilation
ir/            Intermediate representation: declarations, expressions, tables, lookup, optimization passes
vm/            Stack-based bytecode VM (fixed 256-element stack, thread-safe string pool, table lookup)
govern/        Governance primitives: segments, rollouts, kill switches, prerequisites, arbitrace
flags/         Feature flags: variants, schema validation, secret references, hot reload
strategy/      Native decision trees: exactly-one governed routing with arbitrace
expert/        Forward-chaining inference: working memory, assert/emit/retract/modify, temporal constraints
workflow/      Multi-arbiter chaining: outcome→fact mapping, topological ordering, delivery
audit/         Durable decision logging (Sink interface, JSONL default)
overrides/     Runtime governance overrides (kill switches, rollout percentages)
grpcserver/    gRPC service + Prometheus metrics + OTel traces + separate HTTP listener
observability/ Structured logging (slog): standard field set, logger factory
dataplane/     Agent sidecar: local compiled cache, bundle/override watch streams
arbtest/       Test framework: .test.arb files for rules, flags, strategies, expert scenarios
bundle/        Binary bundle serialization with obfuscation, signing, and table support
decompile/     Bytecode → Arishem JSON, ConvertJSON bridge
format/        Canonical formatter with table column alignment
decimal/       Exact fixed-point arithmetic (add, sub, mul, div, mod) with unit validation
units/         85+ units across 19 dimensions with base-unit conversion
module.go      Module resolver: arbiter.toml discovery, import resolution, namespace prefixing
input.go       Compile-time input schema validation
program.go     Unified Program type with functional options
sourceunit.go  Multi-file compilation with module and include support
```

Flat `[]byte` of fixed-width 4-byte instructions: `[opcode(1B), flags(1B), arg(2B)]`. Constant pool indices are `uint16`, giving 65K unique values per type. The parser uses [gotreesitter](https://github.com/odvcencio/gotreesitter), and the repo ships a tree-sitter highlight query and a VS Code language package for `.arb` files.

### Dataplane Agent

`cmd/arbiter-agent` is a localhost sidecar that watches the control plane for bundle and override updates. It caches compiled snapshots locally for subsecond evaluation without network round-trips.

```bash
arbiter-agent --upstream 127.0.0.1:8081 --grpc 127.0.0.1:7081 --bundle-name checkout --bundle-name pricing
```

### WASM Target

`cmd/arbiter-wasm` compiles to WebAssembly for browser and edge evaluation.

```bash
GOOS=js GOARCH=wasm go build -o arbiter.wasm ./cmd/arbiter-wasm
```

Exposes `arbiterCompile`, `arbiterEval`, `arbiterEvalGoverned`, and `arbiterEvalStrategy` to JavaScript. Includes `loader.js` for Node.js and browser environments.

### Typed Evaluation

Go generics map structs directly to evaluation contexts via `arb` struct tags:

```go
type Order struct {
    Total  float64 `arb:"order.total"`
    Region string  `arb:"order.region"`
}

matched, arbitrace, err := arbiter.EvalGovernedTyped(compiled, Order{Total: 150, Region: "US"})
```

### Include Resolver

Include resolution is pluggable via the `IncludeResolver` interface. The default reads from the filesystem; custom implementations can resolve from HTTP, registries, or in-memory sources.

```go
unit, err := arbiter.LoadFileUnitWithResolver("rules.arb", myHTTPResolver)
```

### Multi-Error Recovery

The compiler reports all errors in one pass. Lowering and validation accumulate errors across declarations and return them via `errors.Join`. The CLI and VS Code extension display all diagnostics at once.

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
    kill_switch on
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
    rollout 50
    when segment untrusted_region {
        tx.amount > 100
        and account.has_2fa == false
    }
    then Challenge {
        type: "sms_otp",
        timeout: "5m",
    }
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

**v1.10.0** — language contract frozen, compiler and VM stable, full IDE experience.

What you can rely on:

- Sub-microsecond rule evaluation (223ns/rule, 10K rules in 2ms)
- Module system with namespaced imports and `arbiter.toml` manifests
- Typed declaration family: `input`, `feature`, `fact`, `outcome`, `table`
- Compile-time input schema validation, action param type checking, regex pre-compilation
- Lookup tables for data-driven decisions without rule explosion
- Rule tagging with selective evaluation (`WithTags`)
- Feature flags, strategies, expert inference, continuous arbiters
- Governance primitives: segments, rollouts, kill switches, prerequisites, arbitraces
- Exact decimal arithmetic with 19 unit dimensions (85+ symbols)
- gRPC server with Prometheus metrics, structured logging (`slog`), OpenTelemetry traces
- Full LSP: diagnostics, completions, hover, go-to-def, references, rename, symbols, formatting, semantic highlighting, code actions, multi-file diagnostics
- `.test.arb` test framework, decision diff, audit replay
- Binary bundles with obfuscation and Ed25519 signing
- WASM compilation target
- Node, Python, Rust SDKs

What is evolving:

- Continuous arbiter runtime (poll-based loops, source polling, worker dispatch, delivery retry are shipped; streaming triggers beyond poll are in progress)
- Remote capability runtime ergonomics (`CapabilityService` is shipped for SDK-owned source/sink/worker plugins; richer auth/TLS/registry conventions are still evolving)
- Fact source ecosystem (CSV, JSON, JSONL, HTTP, Terraform, Google Sheets shipped; additional connectors via `Loader`/`Saver` interfaces)
- SDK wrapper libraries (Node, Python, and Rust wrappers now track the current control-plane surface and ship the capability-service contract; higher-level domain ergonomics beyond the gRPC model are still evolving)

Arbiter is maintained by a solo author. Contributions, feedback, and design-partner conversations are welcome.

## License

Apache 2.0
