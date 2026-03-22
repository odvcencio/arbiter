# Changelog

## v0.5.0

### Scientific Rigor

- **Fact and outcome schemas** — `fact` and `outcome` are now first-class top-level declarations with typed fields, optional fields, an implicit `key: string`, compile-time field-access checks, and runtime validation for expert `assert`, `modify`, and `emit` payloads.
- **Quantities and units** — `number<dimension>` fields plus literals like `28 C`, `1200 ppm`, and `5m` now normalize through a built-in unit table, reject dimension mismatches at compile time, and accept runtime `expert.Q(...)` values for schema-aware sessions.
- **Exact decimals** — `decimal` and `decimal<currency|cryptocurrency>` add exact fixed-point values, literal parsing like `1000.25 USD`, VM comparison/add/sub/abs support, and runtime `expert.D(...)` helpers for schema-aware assertions.

### Temporal And Authoring

- **Timestamp expressions** — RFC3339 timestamp literals, `now()`, and timestamp-plus-duration arithmetic now evaluate directly in rule conditions, which lets temporal windows live in the language instead of only in session metadata.
- **Join sugar and richer IR** — `join a: Sensor, b: Sensor on .zone { ... }` now lowers to nested quantifiers with self-join exclusion, and the IR now carries schemas, temporal metadata, quantity/decimal/timestamp literals, and builtin calls for downstream tooling.
- **Workflow session control** — workflows can now replace one arbiter session's base envelope or assert a fact directly into a running arbiter without rebuilding the whole graph.

### Tooling

- **`arbiter test` / `arbtest`** — executable `.test.arb` suites now cover stateless rules, flags, timed expert scenarios, and streamed arbiter scenarios from the CLI or package API.
- **`arbiter explore` / `explore`** — bundles can now be summarized as JSON with schemas, constants, rule metadata, expert timing controls, and the unit dimensions they depend on.
- **Coverage across the new surface** — parser, lowering, compiler, VM, expert runtime, workflow, CLI, and package tests now lock in the schema-aware and temporal feature set end to end.

---

## v0.4.2

### Decision Tooling

- **`arbiter diff`** — compare two governed rulesets against the same JSON context or batch of contexts and report added, removed, and changed rule outcomes by request key. This is the deployment-safety surface for “what changes if we ship this ruleset?”
- **`arbiter replay`** — read audited `kind: "rules"` JSONL decisions, re-evaluate their recorded contexts against a ruleset, and report what would change now. Replay supports request filtering and capped batches for targeted investigations.
- **Audit-stable comparison** — diff/replay normalize empty action params so `{}` vs omitted audit fields do not create fake changes, and the CLI reports include the compared paths for clearer operator output.

### CLI Maintainability

- **Command dispatch cleanup** — `cmd/arbiter/main.go` now routes through per-command handlers instead of one monolithic switch body, keeping the dispatcher readable as the CLI surface grows.
- **Command-layer tests** — new tests cover diff output, replay from audit JSONL, key-path context naming, and param normalization at the release surface rather than only at helper level.

### HTTP Embedding

- **`arbiter.Middleware`** — Go services can now evaluate governed rules directly in `net/http` without a sidecar, with the decision injected into the request context for downstream handlers.
- **Production hooks** — `MiddlewareWithOptions` adds explicit request-context builders plus custom build/eval error handlers, so teams can fail closed, fail open, or map errors into their own response format.
- **Default request context** — `DefaultHTTPContext` exposes normalized request metadata under `request.{method,path,host,headers,query,...}` with header/query key normalization and scalar coercion for the zero-friction path.

### Temporal Windows

- **Wall-clock metadata for facts** — expert facts now carry `asserted_at`, and evaluation contexts expose `__now`, `fact.__asserted_at`, and `fact.__age_seconds` alongside the existing round metadata.
- **Clock-injected sessions** — `expert.Options.Now` lets tests and production runtimes control the session clock explicitly instead of hard-coding `time.Now()`.
- **Time wakes quiescent sessions** — a later clock tick now counts as work for expert evaluation, so a long-lived session can emit age-based outcomes without requiring an unrelated fact mutation to wake it up.

### Bidirectional Fact Sync

- **`Session.SyncFacts`** — long-lived expert sessions can now ingest authoritative source snapshots in one call, asserting new facts, updating changed facts, and retracting disappeared external facts with a concrete sync summary.
- **Write-capable factsource registry** — `expert/factsource` now supports `Save(...)` alongside `Load(...)`, with registered savers for `.csv`, `.json`, `.jsonl`, `gsheet://...`, and `postgres://...`.
- **Google Sheets full replacement writes** — Sheets save paths now clear stale rows before update and require OAuth/service-account auth for writes instead of silently attempting API-key writes that cannot succeed.
- **Transactional Postgres fact sources** — `expert/factsource` now supports `postgres://...` and `postgresql://...` with validated table/column config, row-version loading, serializable writes, and explicit `mode=replace|merge` behavior for authoritative snapshots versus non-destructive upserts.
- **Terraform/HCL fact sources** — `.tf`, `.tfvars`, `.hcl`, and `terraform://...` now load infrastructure definitions as facts using gotreesitter's embedded HCL grammar. Terraform directories are merged deterministically, resources are exposed both as generic `Resource` facts and typed facts like `aws_s3_bucket`, and `terraform://...json` targets ingest `terraform show -json` plans as `Resource` plus `ResourceChange` facts keyed by address.

### Multi-Arbiter Workflows

- **`workflow` runtime package** — chained arbiter declarations now have a real execution layer: compile once, keep one long-lived expert session per arbiter, sync external sources, and run the graph in topological order.
- **Delta-based chaining** — `on Outcome chain target` now forwards only newly emitted upstream outcomes into downstream `source chain://upstream` inputs, which keeps chained arbiters event-driven instead of replaying the entire upstream history on every pass.
- **Runtime validation** — workflow compilation now rejects unknown chain endpoints, mismatched `chain` handlers versus `source chain://...` declarations, runtime writes to `chain://...` sources, and cyclic arbiter graphs.
- **Reliable source polling** — `workflow.NewRunner` adds retry-with-backoff source loads, last-known-good retention when a source is unavailable, and per-source health exposed to rules as `source.<alias>.{available,__source_age_seconds,...}`.
- **Durable sink catch-up** — the same runner can journal pending non-chain deliveries to a local JSONL log, retry failed handlers with backoff, and replay pending deliveries after restart instead of dropping them when a sink is unavailable.
- **Mutable runtime metadata** — expert sessions now support envelope updates between runs, so long-lived arbiters can react to changing source and sink health even when the working-memory fact set itself has not changed.

---

## v0.4.1

### Expert Runtime

- **`per_fact` completed end to end** — the parser/compiler/VM/session path now carries `per_fact` all the way through. Quantifier bindings remain available to action params, and expert sessions track per-target mutation instances so one rule can support multiple fact keys without collapsing to the last firing.
- **Session-loop cleanup** — `expert/session.go` now splits round application and inactive-mutation cleanup out of `Run`, cutting the file hotspot from `cog=77` to `cog=32` while keeping the inference behavior intact.

### Fact Sources

- **Google Sheets loader** — `expert/factsource` now supports `gsheet://SPREADSHEET_ID/SheetName` through the Sheets Values API, with API key, bearer token, or service-account auth from environment variables.
- **Shared tabular mapping** — CSV and Google Sheets now share one header-to-fact mapping path, and the factsource adapters consistently expose `key` inside fact fields for rule access across CSV, HTTP, JSON, JSONL, and Sheets.

---

## v0.4.0

### Continuous Arbiters

- **Fourth modality** — `arbiter Name { ... }` is now a first-class top-level declaration for continuously running decision loops. `CompileFull` extracts arbiters alongside rules and segments, with implicit killability and validation for duplicate names, invalid chain targets, zero poll intervals, and handler shape.
- **Trigger and routing surface** — arbiters support `poll`, `schedule`, `stream`, `source`, `checkpoint`, `chain`, and handler `where` clauses directly in `.arb`. Slack channel targets are now a distinct lexical token, so `slack #alerts` works without regressing `#comment` syntax.
- **Docs and examples** — the README and pipeline example now describe and exercise the continuous-arbiter declaration surface without pretending the whole transport stack is already runtime-wired.

### Data Plane

- **`arbiter-agent` sidecar** — a localhost-serving data plane that bootstraps active bundles from upstream, watches bundle and override streams, keeps local compiled snapshots hot, and exposes `/healthz`, `/readyz`, and `/status`.
- **`dataplane` package** — the old package name `agent` has been retired in favor of `dataplane`, matching the code's actual role instead of overloading the language term `arbiter`.
- **Fact source adapters** — expert sessions now have a pluggable `expert/factsource` package covering CSV, HTTP, JSON, and JSONL inputs.

### Serving And SDKs

- **Streaming bundle APIs** — gRPC now exposes `GetBundle`, `WatchBundles`, `GetOverrides`, and `WatchOverrides`, plus the corresponding server/runtime plumbing and SDK surface updates.
- **Local test harness** — example suites no longer depend on a live cluster by default; they can spin up an in-memory gRPC path for deterministic local verification.

### Quality

- **Race-stable readiness tests** — dataplane and status tests now allow enough headroom to pass under full `go test -race ./...` contention instead of failing at the suite boundary.
- **Include-aware arbiter diagnostics** — semantic errors for arbiters declared in included files now map back to the original source file consistently.

---

## v0.3.0

### Language

- **Aggregate expressions** — `sum(expr for var in collection)`, `count(var in collection)`, and `avg(expr for var in collection)` as first-class expressions. Work anywhere a value is expected: conditions, action params, expert rules. Three new opcodes (`OpAggBegin`, `OpAggAccum`, `OpAggEnd`) with iterator-style accumulation on the bytecode VM's fixed stack.
- **Let bindings** — `let name = expr` declarations inside `when { }` blocks. Bound names are available in subsequent condition expressions and in action parameter expressions. Compiles to `OpSetLocal` which stores the evaluated result in the VM's locals map, resolved by `OpLoadVar` before the data context.
- **String concatenation** — the `+` operator now concatenates when either operand is a string. Mixed types are coerced to string. Enables `message: "User " + user.name + " exceeded limit"` in action params.
- **Flag `else when` chains** — `else` keyword before `when` in flag rules for explicit intent. Pure syntax sugar — flag evaluation is already first-match-wins. Makes rule ordering intent readable.

### Expert Inference

- **`stable` keyword** — expert rules marked `stable` are deferred until the system reaches a local fixed point (no mutations in the previous round). Eliminates the need for manual two-phase gating when checking for absence of facts. The session forces an extra quiet-round evaluation pass before quiescence to give stable rules a chance to fire.
- **Temporal fact conditions** — every fact now carries `AssertedRound` metadata tracking which round it was first asserted. Exposed as `__round` in the fact's fields and `current_round` in the top-level evaluation context. Enables rules like `any f in facts.Marker { f.__round < current_round - 3 }` for staleness checks.

### Testing

- Aggregate test coverage in `eval_language_features_test.go` for sum, count, and avg across nested object collections.
- Let binding tests verifying local availability in both conditions and action params.
- Stable rule tests in `expert/session_internal_test.go` verifying deferred scheduling across quiescent rounds.
- String concatenation tests for string+string, string+number, and number+string coercion.
- Flag else-when chain tests in `flags/flags_test.go`.
- Temporal round tracking tests verifying `__round` and `current_round` in expert session evaluation.
- Multi-quantifier `and` regression test in `grammar_test.go` locking in existing parser behavior.

---

## v0.2.0

### Language

- **`excludes` keyword** — negative rule gating. A rule with `excludes OtherRule` only fires if `OtherRule` did not match. Works in both stateless rules and expert inference. Enables patterns like "fertilize only when not in drought" without duplicating conditions.
- **Flag segment+inline combo** — flag rules now support `when segment_name { condition }` to combine a segment gate with an inline condition. Previously flags required either a segment reference or an inline condition, not both.
- **Order-independent `activation_group` and `requires`** — expert rules now accept `requires` and `activation_group` in any order. Previously `activation_group` before `requires` caused a parse error.
- **UTF-8 comments** — `#` and `//` comments now support full Unicode including emoji, CJK, and extended Latin characters.

### Flags

- **Environment overlays** — `LoadFileWithEnv("flags.arb", "production")` loads a base file and merges `flags.production.arb` on top. Flags in the overlay replace base definitions by key. Flags only in the base are kept. Flags only in the overlay are added. Segments merge additively.
- **Assignment events** — every non-default flag resolution emits a `FlagAssignment` audit event containing flag, variant, user ID, environment, and payload values. Designed for experimentation pipelines: join on user ID in your analytics warehouse to compute variant lift.
- **Environment field** — `Flags.Environment` is set by `LoadEnv` and `LoadFileWithEnv`, propagated to all audit events (`DecisionEvent.Environment`, `FlagDecision.Environment`).

### Compiler

- **Fixed short-circuit jump backpatching** — `a and (b or c)` and `not (a and b)` previously evaluated incorrectly. The compiler's jump distance for `OpJumpIfFalse`/`OpJumpIfTrue` landed on the combining opcode instead of past it, causing stack corruption on short-circuit. Fixed by computing `len(code) - jumpPos` instead of `len(code) - jumpPos - InstrSize`.

### Expert Inference

- **`excludes` in expert rules** — expert rules support `excludes` with deferred evaluation. If an excluded rule hasn't been evaluated yet in the current round, the excluding rule is skipped until a later round when the result is known.

### Governance

- **`CheckExclusions`** — new governance cache method that verifies no excluded rules matched. Returns false if any exclusion matched or if an excluded rule hasn't been evaluated yet.
- **`SegmentSet.All()`** — returns all compiled segments for environment overlay merging.

### Deployment

- **Kubernetes manifests** — `deploy/Dockerfile` and `deploy/k8s.yaml` for deploying Arbiter as an in-cluster gRPC service. 3 replicas at 1 core each delivers 41K evals/sec with sub-2ms p50 latency.
- **Deploy script** — `scripts/deploy.sh` for the Orchard platform with pre-flight postgres checks.

### Examples

- **Greenhouse plant management** — 17 expert rules demonstrating sensor-driven inference with soil moisture, nutrition, humidity, temperature, CO2 monitoring, two-phase gating for all-clear detection, and `excludes` for conditional action suppression.
- **LaunchDarkly-equivalent flag suite** — 7 flags across 9 segments covering boolean flags, multivariate flags, progressive rollouts, prerequisites, kill switches, variant payloads, segment+inline combos, runtime overrides, explain traces, and edge cases. 30 test scenarios.
- **CI governance gateway** — webhook handler that evaluates `.arb` rules against GitHub Actions billing data to govern workflow runs by budget, branch, time, and rate limits.
- **Fraud detection** — 8 stateless rules with segments for high-risk geo, trusted accounts, new accounts, velocity checks, and currency mismatch detection.

### Highlights

- **Syntax highlighting fixes** — `highlights.scm` updated for governance keywords (`kill_switch`, `requires`, `excludes`, `rollout`, `no_loop`, `activation_group`), expert blocks (`expert_when_block`, `expert_where_block`, `expert_binding`), and expert action kinds (`assert`, `emit`, `retract`, `modify`). Fixed node-level captures for named child nodes vs anonymous strings.

---

## v0.1.0

Initial release.

### Language

- **Rules** — `rule Name priority N { when { condition } then Action { params } }` with `otherwise` fallback, `kill_switch`, `requires` prerequisites, `rollout` percentage gates, and `when segment name` segment gates.
- **Expert rules** — `expert rule Name { when { condition } then assert/emit/retract/modify Target { params } }` with forward-chaining inference until quiescence. Truth maintenance via reversible overlays: assert creates priority-based supports, retract hides facts, modify overlays field updates. All three revert when the supporting rule stops matching.
- **Feature flags** — `flag name type boolean/multivariate default "value" { variant "name" { payload } when condition then "variant" rollout N }` with segments, prerequisites, kill switches, typed variant payloads, schema validation, and secret references.
- **Segments** — reusable named conditions shared across rules and flags.
- **Constants** — compile-time inlined values (`const NAME = value`).
- **Includes** — multi-file compilation with `include "path.arb"`, cycle detection, and error mapping to original source files.
- **Features** — data source declarations with typed fields.
- **Operators** — comparison, logical (short-circuit `and`/`or`/`not`), collection (`in`, `contains`, `retains`, `subset_of`, `superset_of`, `vague_contains`), string (`starts_with`, `ends_with`, `matches`), null checks, range (`between` with open/closed brackets), math (`+`, `-`, `*`, `/`, `%`), quantifiers (`any`, `all`, `none`).
- **Expert bindings** — `bind var in facts.Type where { join condition }` compiles to nested existential quantifiers for cross-fact correlation.
- **Expert controls** — `no_loop`, `activation_group`, `kill_switch`, `requires`, `rollout`.

### Compiler

- Bytecode compiler with 47 opcodes in a flat `[opcode(1B), flags(1B), arg(2B)]` encoding.
- Constant pool (`intern.Pool`) deduplicates all strings and numbers. 10K rules referencing the same field names share one copy.
- Two-pass compilation: collect constants, then emit bytecode with backpatched jump distances.

### VM

- Fixed 256-element stack machine. `96 B/op`, `3 allocs/op` per rule evaluation.
- ~223ns single rule eval. 72MB for 10K compiled rules (vs 7.8GB for Arishem).
- Iterator opcodes with nested depth tracking for quantifier evaluation.
- Regex caching for `matches` expressions.

### Expert Inference

- Forward-chaining inference loop with configurable `MaxRounds` (default 32) and `MaxMutations` (default 1024).
- Four action kinds: `assert` (priority-based supports), `emit` (deduplicated outcomes), `retract` (hide facts), `modify` (field overlays with `set { }` blocks).
- Reversible overlays with truth maintenance. `desiredFact()` computes visible state from supports, retractions, and modifications. `recomputeFact()` propagates changes.
- Selective re-evaluation via dirty tracking. `shouldEvaluate()` only wakes rules whose fact dependencies or prerequisites changed.
- Evaluation context isolation: `evalContextIgnoringOwnMutation()` prevents rules from seeing their own effects when re-evaluating.
- Activation groups for mutual exclusion within a round.
- Provenance tracking via `DerivedBy` field on every fact.
- Checkpoint and `DeltaSince()` for incremental result streaming.

### Governance

- Segments compiled to bytecode, evaluated once per request via `RequestCache` memoization.
- Deterministic rollout bucketing: `SHA256(userID)[:4] % 100`.
- Kill switches, prerequisites with cycle detection, explainability traces.
- Runtime overrides for kill switches and rollout percentages without recompiling.

### Flags

- Boolean and multivariate flags with typed variant payloads.
- Schema validation at load time (type consistency across variants).
- Secret references (`secret("ref")`) preserved for core eval, redacted in explain/HTTP.
- Hot reload via `fsnotify` file watcher across the include graph.
- HTTP handler serving `/flags` and `/explain` endpoints.
- `LoadEnv(dir, env)` for per-environment flag files.

### Serving

- gRPC API: `PublishBundle`, `ListBundles`, `ActivateBundle`, `RollbackBundle`, `EvaluateRules`, `ResolveFlag`, `StartSession`, `RunSession`, `AssertFacts`, `RetractFacts`, `GetSessionTrace`, `CloseSession`, `SetRuleOverride`, `SetFlagOverride`, `SetFlagRuleOverride`.
- Bundle versioning with per-name history, activation, and rollback. SHA256 checksums. File-backed persistence.
- Session store with 30-minute TTL, LRU eviction at 10K sessions, per-session mutexes.
- Audit sink interface with JSONL default. Every decision logged with full context, trace, and timestamps.

### Transpilation

- Emit to Rego (OPA), CEL, and Drools DRL with target-idiomatic output.
- Decompile bytecode back to Arishem JSON.
- Arishem JSON import via `CompileJSONRules` for migration.

### Authorization

- Thin ABAC helper: `authz.EvaluateSource(source, Request{Actor, Action, Resource})` standardizes context and checks for `Allow` actions.

### CLI

- `arbiter compile`, `arbiter eval`, `arbiter check`, `arbiter emit`, `arbiter expert`, `arbiter serve`, `arbiter import`.
- File-aware diagnostics with `path:line:column` error formatting across includes.

### Editor Support

- Tree-sitter grammar (`grammar.json`, `grammar.bin`) and highlight query (`highlights.scm`) for `.arb` files.
- VS Code extension with syntax highlighting, snippets, and `arbiter check` diagnostics on open/save.

### SDKs

- Generated gRPC clients for Node.js, Python, and Rust in `sdks/`.

### Performance

| Metric | Arishem | Arbiter | Factor |
|--------|---------|---------|--------|
| 10K rule compile memory | 7.8 GB | 72 MB | 108x less |
| 10K rule allocations | 153M | 940K | 163x fewer |
| 5K rule eval memory | 3.9 GB | 160 KB | 24,375x less |
| Single rule eval | ~1.4ms | ~223ns | ~6,300x faster |

| Engine | ns/op | B/op | allocs/op |
|--------|-------|------|-----------|
| Arbiter | 223 | 96 | 3 |
| CEL | 82 | 24 | 2 |
| OPA/Rego | 5,680 | 6,444 | 114 |
