# Changelog

## Unreleased

## [1.10.0] — 2026-09-21

### Added

- **Prepared tag-filtered evaluation** — `vm.NewPreparedEvaluator` selects an ordered rule window once and reuses one bounded VM across evaluations. It preserves the existing tag, active-window, action, error, and output semantics.

## [1.8.1] — 2026-06-10

### Added

- **Segment-only rule form** — `when segment NAME` (no `{ ... }` block) is now valid syntax. Previously this was a parse error; the only accepted form was `when segment NAME { expr }`. Use it when the segment gate is the entire condition and no inline predicate is needed.

### Fixed

- **Engine-wide empty-condition consistency** — a rule with no inline condition is now uniformly treated as unconditional (always-passes) across all eval entrypoints: `EvalGoverned`, `Eval`, `EvalDebug`, expert inference, and strategy paths. Previously the behaviour was inconsistent: on some eval paths an empty-condition rule silently never fired rather than firing unconditionally, meaning segment-only rules could be parsed (where the grammar allowed them) but would not produce outcomes at runtime.
- **Regression tests** — `TestBundleRoundTripConditionGating` pins the bundle round-trip fail-open path: a rule with a real condition must still be gated by it after `Marshal`/`Unmarshal`. `TestCompileJSONConditionGating` pins the `CompileJSON` (Arishem loader) path. `TestSegmentOnlyRuleCrossPaths` and `TestSegmentShapeMatrix` verify that `when segment NAME` fires under `EvalGoverned`, `Eval`, and `EvalDebug` regardless of rule order or neighbours.

### Notes

- Investigated a field report of two inline-condition rules starving a following segment rule. Not reproducible at v1.8.0 with correct `.arb` syntax; the actually-broken case was the segment-only form (`when segment NAME` without braces), which was a parse error before this release.

## [1.8.0]

- (stub — see git log for full content)

## v1.5.0

### Runtime Capability Plugins

- **Custom sink and worker kinds** — arbiter handler clauses and worker runtime clauses now accept host-registered identifiers instead of only the built-in transport list. `chain` and `worker` remain reserved runtime kinds, while `chain://...` and `worker://...` remain runtime-owned source schemes.
- **Portable capability protocol** — a new gRPC `CapabilityService` contract lets non-Go hosts implement source loaders, sink handlers, and worker runtimes over a shared SDK-facing protocol.
- **Reference runtime hook-up** — `arbiter-runtime --capability-grpc ...` now dials one remote capability service, binds its declared source schemes, sink kinds, and worker kinds into the workflow runner, and exposes that manifest on `/status`.
- **Explicit worker-runtime boundary** — sink handlers no longer implicitly count as worker runtimes. Workers stay typed capabilities, so a worker kind must be explicitly registered on the worker-runtime surface before a bundle can run.
- **Unified runtime capability status** — the workflow runner now exposes one inspectable capability surface across sources, sinks, and worker runtimes, tagged by owner (`core`, `host`, or `plugin`) instead of only dumping raw plugin manifests.
- **Runtime gRPC introspection** — `arbiter-runtime` can now serve `RuntimeService.GetRuntimeCapabilities`, and the CLI plus Node/Python/Rust SDKs expose that same unified capability surface without scraping HTTP `/status`.
- **Unified transport hardening** — `arbiter-runtime --grpc` now uses the same bearer-token and TLS/mTLS hardening model as `arbiter serve`, instead of exposing a separate plaintext-only control surface.
- **Explicit plugin transport** — capability-plugin dialing now uses the same target grammar as the rest of Arbiter (`grpc://`, `grpcs://`, `http://`, `https://`, or bare `host:port`) plus explicit token, CA, server-name, and plaintext flags.
- **Inspectable runtime posture** — `/status` and `RuntimeService.GetRuntimeCapabilities` now expose the runtime control surface's effective auth/TLS/public-listener posture and the bound capability-plugin connection's target/auth/TLS/server-name posture, so operators can inspect the real runtime shape instead of reconstructing it from flags.
- **Inspectable agent posture** — `arbiter-agent /status` now exposes local control-listener posture, upstream transport posture, readiness policy/reason, and per-bundle bundle/override watch connectivity alongside the existing sync counters.
- **Agent transport hardening parity** — `arbiter-agent` now supports the same local bearer-token and TLS/mTLS hardening shape as `arbiter-runtime` on its local gRPC surface, so the reported control posture is backed by real configuration instead of placeholders.
- **Canonical operator surfaces** — `arbiter-runtime /status`, `arbiter-agent /status`, and `arbiter runtime-capabilities` now use the same small inspection vocabulary and section order instead of mixing flat counters, ad hoc headings, and transport-specific nouns.
- **Status over gRPC** — the canonical operator surfaces now travel over RPC too: `RuntimeService.GetRuntimeStatus` exposes `readiness -> issues -> transport -> capabilities -> activity`, `AgentService.GetAgentStatus` exposes `readiness -> issues -> transport -> sync`, `ControlService.GetControlStatus` exposes `readiness -> issues -> transport -> bundles -> overrides -> sessions -> audit`, and the CLI now ships `arbiter runtime-status`, `arbiter agent-status`, plus `arbiter control-status`.
- **Hosted control-plane introspection** — `arbiter serve` now exposes real `/status` output instead of identity-only JSON, including listener auth/TLS posture, persisted bundle/override files, active bundle versions, live expert-session occupancy, and explicit audit posture (`jsonl` vs discard, durable vs non-durable).
- **Audit health, not just audit config** — the hosted control-plane `audit` section now reports whether recording is currently healthy, plus write/error counters and the last successful or failed audit write timestamps, so operators can see when decision recording is configured but not actually succeeding.
- **Persistence health, not just persistence config** — the hosted control-plane `bundles` and `overrides` sections now report whether file-backed state is currently healthy, plus write/error counters and the last successful or failed persistence write timestamps, so operators can see when durable state is configured but not actually sticking.
- **Readiness follows durability health** — hosted control `/readyz` now uses the same readiness judgment as the `readiness` section, so configured bundle persistence, override persistence, or audit recording failures take the control plane out of readiness instead of leaving probes green.
- **Canonical issue lists** — runtime, agent, and hosted control status now all expose an explicit `issues` surface with shared `severity`, `scope`, `subject`, `code`, `message`, and `blocking` fields, so operators and SDKs can inspect concrete failures and insecure transport posture without reconstructing them from mixed booleans and free-form reasons.
- **CLI issue gating** — `arbiter runtime-status`, `arbiter agent-status`, and `arbiter control-status` now accept `--fail-on-issues`, so automation can fail fast on blocking status issues without scraping terminal output.
- **Issue-code contract** — the status issue vocabulary is now centralized and documented, so `issues.code` is an explicit operator-facing contract instead of a scattered implementation detail.
- **Issue catalog surfaces** — the canonical status-issue vocabulary now carries explicit runtime/agent/control surface membership and is exposed directly through `arbiter status-issues` plus `GetStatusIssueCatalog` on runtime, agent, and control gRPC services.
- **HTTP issue catalogs** — runtime, agent, and hosted control now also expose scoped `GET /status/issues` JSON catalogs, so HTTP-first operators can inspect the same issue contract without gRPC tooling.
- **Remote CLI issue catalogs** — `arbiter status-issues` now accepts a live target plus auth/TLS flags, auto-detects runtime vs agent vs control surfaces, and prints the remote-advertised issue catalog instead of assuming the local CLI build matches the running process.
- **Versioned operator identity** — runtime capability/status, agent status, hosted control status, and issue-catalog surfaces now advertise `operator.product`, `operator.build_version`, and `operator.operator_contract_version`; the CLI prints that contract stamp on every remote inspection command and can fail on mismatches via `--fail-on-contract-mismatch`, so tooling can verify build and operator-contract compatibility directly from a live process.

### SDKs

- **SDK surface parity** — the shipped Node, Python, and Rust clients now track the current gRPC control-plane surface instead of lagging behind it. Strategy evaluation, strategy-candidate override mutation, structured `ArbitraceStep` fields, and explicit `kill_switch_state` now flow through the vendored SDK contracts as well.
- **Runtime and agent status clients** — the shipped Node, Python, and Rust wrappers now expose runtime-status and agent-status RPCs alongside runtime-capability introspection, so embedders do not need to scrape HTTP `/status`.
- **Hosted control-status clients** — the shipped Node, Python, and Rust wrappers now also expose `ControlService.GetControlStatus`, so hosted control-plane introspection is not Go-only.
- **Status-issue catalog clients** — the shipped Node, Python, and Rust wrappers now also expose `GetStatusIssueCatalog`, so automation can inspect the issue vocabulary itself instead of hardcoding string lists.
- **SDK operator contract guards** — the Node, Python, and Rust clients now expose operator-contract inspection helpers plus optional strict compatibility enforcement on runtime-capability, runtime-status, agent-status, control-status, and status-issue catalog RPCs.
- **Capability-service mirrors** — the Node, Python, and Rust SDK trees now mirror the runtime capability proto as well, so embedders can implement source/sink/worker plugins in those languages instead of being limited to Go interfaces.
- **Capability server helpers** — Node and Python now ship `CapabilityServer` helpers, and Rust ships `CapabilityPlugin` plus handler traits, so SDK authors can register source/sink/worker behavior without hand-writing raw gRPC service plumbing.

### Docs

- **Authoring house style** — the README now gives a firmer opinion on `.arb` layout: typed declarations first, shared governance next, one dominant decision surface per file, runtime-edge modules at the boundary, explicit split triggers, inward import direction, colocated `.test.arb` files, and canonical `governance -> match/bind -> effect` clause order across modalities.

## v1.4.0

### Language Shape

- **Canonical governance prelude order** — rules, strategies, flags, and expert rules now read as governance prelude, then matching/binding, then effect. `rollout` now lives before `when` across governed declarations.
- **Explicit kill-switch state** — `kill_switch on` and `kill_switch off` are preserved through compile, bundle, runtime, hover, explain, and explore surfaces. Kill-switch state is no longer collapsed to a single bool.
- **Typed declaration family** — `input`, `feature`, `fact`, `outcome`, and `table` are now documented and surfaced as one typed-data family in inspection output.
- **Authoring doctrine** — the README now recommends a canonical `.arb` file shape, modality choice doctrine, and module split strategy for readable bundles.
- **Structured observability vocabulary** — governed arbitraces now carry shared `phase`, `scope`, `subject`, `kind`, `check`, `result`, and `detail` semantics across rules, flags, and strategies; the same structured `ArbitraceStep` shape now flows through gRPC responses; and expert activations include their own per-firing arbitrace in both API and audit surfaces.
- **Explicit override kill-switch state** — override snapshots, watch events, and audit override mutations now expose canonical `kill_switch_state` (`on`, `off`, or unset) instead of forcing operators to reconstruct intent from paired bool fields.

### VM / Runtime

- **Lower-allocation data lookup** — exact-key context lookups fast-path, dotted path resolution no longer allocates via `strings.Split`, and string-pool reads use a lock-free snapshot.
- **Measured governed-eval wins** — the rule-heavy benchmark suite improved by roughly 15-47% wall time with substantial allocation reductions after the VM/data-context pass.

## v1.3.0

### Rule Tagging

- **`tag`/`tags` declarations** — top-level tag declarations with closed-set validation. Referencing an undeclared tag is a compile error with "did you mean?" suggestions. Unused tags warn.
- **`tag`/`tags` on rules** — `rule R tag "fraud" { ... }` or `rule R tags "fraud,realtime" { ... }`. Works on rules, expert rules, and flag declarations.
- **`WithTags` eval option** — `arbiter.Eval(prog, dc, arbiter.WithTags("fraud"))` evaluates only rules with all specified tags. AND semantics. Zero cost when not filtering.

### Temporal Constraint Composition

- **Valid combinations documented and tested** — `cooldown` composes with any one of `for`, `within`, `debounce`, `stable_for`.
- **Invalid combinations are compile errors** — `for` + `within`, `for` + `debounce`, `within` + `debounce`, and all time-vs-cycle mixing (`for`/`within`/`debounce` + `stable_for`). Duplicate modifiers also rejected.

### LSP — Semantic Highlighting

- **Semantic tokens** for gaps not covered by tree-sitter grammar: fact/outcome names in `assert`/`emit` (type), table names in `lookup` (struct), member access fields (property), qualified name module prefixes (namespace).

### LSP — Code Actions

- **Add missing outcome fields** — quick fix inserts required fields with typed placeholder values.
- **Add `else` to lookup** — quick fix inserts else block matching the table's column schema.
- **Add `requires` prerequisite** — refactoring action inserts `requires` clause for qualified references.
- **Import quick fix** — inserts `import` for unresolved qualified names with multi-candidate support.

### LSP — Multi-File Diagnostics

- **Cross-file error propagation** — errors in imported modules surface in both the imported file and at the `import` line in the importing file.
- **Reverse dependency tracking** — saving a file recompiles all files that import it, refreshing diagnostics across the workspace.

### VS Code Extension

- **Format on save** enabled by default for `.arb` files. Users can override in settings.

---

## v1.2.0

### Action Param Type Checking

- **Rule actions validated against outcome schemas.** When a rule's `then` action name matches a declared `outcome`, field names, types, and required fields are checked at compile time. Unknown action names pass through unvalidated (backward compatible).

### Compile-Time Regex

- **Regex patterns validated and pre-compiled at compile time.** Invalid literal patterns in `matches` expressions are compile errors. Valid patterns are stored pre-compiled for O(1) lookup at eval time, eliminating first-eval latency. Variable patterns still compile at runtime via the existing per-VM cache.

### Lookup Tables

- **`table` declarations** — named, immutable, typed collections of rows. Pipe-separated header + data rows with compile-time type validation. Importable across modules.
- **`lookup` expressions** — query tables with `where` filtering, `order by` sorting, and `else` fallback. Returns first matching row. Deterministic: ties resolve by declaration order.
- **`let` in action blocks** — new semantic expansion. Local bindings in `then`/action blocks, evaluated top-to-bottom when the action fires. Enables `let row = lookup ... else {...}` pattern.
- **Formatter auto-aligns table columns** — `arbiter fmt` and LSP formatting pad pipe-separated columns to consistent widths.
- **Warnings** — `lookup` without `else` warns at compile time. Table column names that shadow input schema root fields warn.
- **Bundle support** — tables serialize into the binary bundle format. v1.2.0 bundles are not backward-compatible with v1.1.0 consumers.

### Observability

- **Prometheus metrics** — `arbiter_eval_total`, `arbiter_eval_duration_seconds`, `arbiter_rule_matches_total`, `arbiter_expert_rounds_total`, `arbiter_expert_mutations_total`, `arbiter_flag_resolves_total`, `arbiter_bundle_publish_total`, `arbiter_active_sessions`. Cardinality-safe: `bundle_name` labels (not `bundle_id`), `status` labels (`ok`/`error`).
- **Separate HTTP listener** — `/metrics`, `/healthz`, `/readyz`, `/status` on a dedicated HTTP port. gRPC port for API only.
- **Structured logging (`slog`)** — JSON output by default. Standard field set: `bundle_name`, `bundle_id`, `mode`, `strategy`, `worker`, `arbiter`, `source`, `handler_kind`, `error`, `request_id`. Configurable via `--log-level` flag or `ARBITER_LOG_LEVEL` env var.
- **OpenTelemetry trace propagation** — eval spans (`arbiter.eval.governed`, `arbiter.eval.strategy`, `arbiter.flag.resolve`) with `bundle_name`, `match_count`, `strategy.selected`, `flag.variant` attributes. Runtime spans (`arbiter.runtime.tick`, `arbiter.worker.dispatch`). One span per eval call — no per-rule spans. OTel dependency on server/runtime only, zero in core library.

### LSP

- **Table support** — table declarations appear in document symbols. Table/lookup validation errors and warnings surface as diagnostics. Table formatting via LSP auto-aligns columns.

### Conformance

- **Action type checking** — validates schema-gated programs reject invalid fields.
- **Regex pre-compilation** — pre-compiled regex produces identical results to runtime-compiled across all surfaces.
- **Table round-trip** — table programs produce identical results across native eval and bundle round-trip.
- **Lookup determinism** — same table + context = identical results every time.
- **Lookup null behavior** — null propagation and else fallback verified across surfaces.

---

## v1.1.0

### Module System

- **`import "path"` / `import "path" as alias`** — namespaced module imports. Declarations in imported files are accessed via `namespace.Name` (e.g., `requires scoring.BaseRule`, `segment scoring.HighRisk`). Last path segment is the default namespace; `as` provides an explicit alias.
- **`arbiter.toml`** — project manifest at the project root. Import paths resolve relative to the manifest directory. Presence of `arbiter.toml` marks the project root, like `go.mod` for Go.
- **Qualified references everywhere** — `requires`, `excludes`, `segment`, and `flag requires` all accept dotted names. Cross-module prerequisites use the result cache, not module evaluation order.
- **Cycle detection, diamond dedup, namespace collision detection** — circular imports error, shared dependencies compile once, duplicate namespaces error with guidance to use aliasing.
- **`include` deprecated** — still works in v1.1.0 with a compiler warning. A file cannot use both `import` and `include`. Removed in v2.0.0.

### Input Schema Validation

- **`input { ... }` block** — declares the expected shape of request data. Supports nested objects, optional fields (`name?: type`), `list<T>`, all existing scalar and dimensioned types. One `input` block per module.
- **Compile-time path validation** — when `input` is declared, all path references in rules are checked against the schema. Unknown paths and type mismatches are compile errors. Absent `input` preserves v1.0.0 behavior (runtime null coercion).
- **Cross-module scoping** — each module validates against its own `input` schema. Type conflicts on overlapping paths across imported modules are compile errors.

### Compile API

- **`Compile(src, ...Option) → *Program`** — single entry point replacing `Compile`, `CompileFull`. Returns a unified `Program` with Ruleset, Segments, Strategies, Expert, IR, Input, and Warnings.
- **`CompileFile(path, ...Option) → *Program`** — file-based compilation with automatic `arbiter.toml` discovery and import resolution. Functional options: `WithManifest`, `WithResolver`.
- **String pool sealed** — `DataFromMap`, `DataFromJSON`, `DataFromStruct`, and all `Eval*` functions accept `*Program`. Pool management is internal. `vm.EvalWithPool`/`vm.EvalDebugWithPool` deprecated.
- **`ConvertJSON` / `ConvertJSONRules`** — permanent bridge functions converting Arishem JSON to `.arb` source bytes for programmatic migration.
- **All v1.0 functions deprecated** — `CompileFull`, `CompileFullFile`, `CompileJSON`, `CompileJSONRules`, `CompileStrategies*`, `CompileResult` type retained as thin wrappers. Removed in v2.0.0.

### LSP

- **Import resolution in diagnostics** — LSP uses `CompileFile` for on-disk files, enabling import path resolution and input schema validation in the editor.
- **Compiler warnings** — `include` deprecation warnings surface as LSP warning-severity diagnostics.

### Conformance

- **Import round-trip** — cross-module programs produce identical results across native eval, governed eval, and bundle round-trip.
- **Input schema parity** — programs with `input` blocks produce identical runtime results to equivalent programs without.
- **API parity** — `Compile` and deprecated `CompileFull` produce identical eval results across all conformance cases.
- **Cross-module expert inference** — expert rules fire based on working memory regardless of module boundaries.

---

## v1.0.0

### Language Specification (Frozen Contract)

- **SPEC.md** — formal language reference with two-tier freeze. Frozen: rule/strategy/flag/expert evaluation semantics, schema/type behavior, decimal/unit rules, governance algorithm, arbitrace shape guarantees, `.test.arb` assertions, bytecode format, conformance matrix. Provisional: runtime surface beyond poll, worker transport breadth, LSP completeness, SDK ergonomics, packaging/module story.

### Formatter

- **`arbiter fmt`** — canonical formatter for `.arb` files. 4-space indentation, consistent brace placement, blank lines between declarations, trailing whitespace removal, trailing newline. `--check` for CI (exits 1 if unformatted). Wired into LSP as `textDocument/formatting`.

### Bundle Signing

- **Ed25519 bundle signatures** — `arbiter bundle --sign key.pem` signs the binary bundle. `arbiter bundle --verify file.arbb --pub key.pub` verifies. Signature trailer (64-byte Ed25519 + "ARBS" magic) appended to bundle. Optional metadata trailer with compiler version, conformance profile, and creation timestamp.

### LSP Navigation

- **Go-to-definition** — jump to any declaration (rule, fact, outcome, segment, strategy, flag, expert rule, worker, arbiter) using IR Span positions.
- **Find references** — finds declaration site + all prereq/exclude/segment references.
- **Rename** — whole-word rename across the current file with word-boundary checking.
- **Document symbols** — outline of all declarations with kinds and positions.
- **Formatting** — `textDocument/formatting` via the `format` package.

---

## v0.14.0

### Language Server

- **`cmd/arbiter-lsp`** — LSP implementation over stdin/stdout with diagnostics (multi-error), completions (facts, outcomes, segments, strategies, rules, keywords), and hover (schema fields, rule summaries). VS Code extension updated to use LSP when available, falls back to CLI check.

### Conformance Suite

- **`conformance/`** — cross-platform parity matrix. Every test case produces identical results across native eval, governed eval, bundle round-trip, obfuscated bundle round-trip, JSON round-trip, strategy eval, and expert inference. 7 test cases × 5 surfaces + strategy + expert.

### Policy-Gated Edge Export

- **Arbiter governs its own export safety.** The bundle command now uses a static analyzer → Arbiter policy pipeline instead of heuristic lint. Analyzer extracts structured signals (`threshold_literal`, `money_literal`, `crypto_literal`, `risk_path`, `prereq_chain`, `rollout_usage`). Default `edge_export_policy.arb` blocks money/crypto/risk, warns on thresholds, allows config. `--risk-policy custom.arb` lets users swap the policy. `--force` overrides blocks.

---

## v0.13.0

### Reference Runtime

- **`cmd/arbiter-runtime`** — standalone host process for continuous arbiters and workers. Loads a `.arb` file, compiles the workflow, and runs the arbiter loop with source polling, worker dispatch (exec, webhook), delivery retry with exponential backoff, and health endpoints (`/healthz`, `/readyz`, `/status`). Continuous arbiter execution is now fully self-contained.

### WASM SDK

- **Full four-mode WASM SDK** — expert sessions (`startSession`, `assertFact`, `retractFact`, `runSession`, `closeSession`) and workflows (`compileWorkflow`, `setSourceFacts`, `runWorkflow`) added to the WASM module. TypeScript types for the full API. 6.1MB gzipped with all four evaluation modes.
- **`loadBundle`** — WASM SDK can load pre-compiled binary bundles (base64) without exposing `.arb` source.

### Binary Bundle Format

- **`bundle/` package** — binary serialization and obfuscation for compiled rulesets. Hashes rule/segment names, strips rollout details and prerequisites. Action names and param keys preserved for result interpretation.
- **`arbiter bundle` CLI** — exports `.arbb` files. Always obfuscates. Fails with a hard error if business-logic patterns are detected (numeric thresholds, fraud/risk/price variable paths, monetary values). `--force` overrides the gate.
- **Edge bundle lint** — heuristic analysis warns when rules look like business logic rather than config/flags.

---

## v0.12.0

### Decimal Arithmetic

- **Multiply, divide, and modulo for exact decimals** — `price * quantity`, `total / count`, and `amount % threshold` now work with `decimal` values (e.g., `49.99 USD * 3 = 149.97 USD`). Unit propagation follows: unitless * unit → unit, same-unit / same-unit → unitless. Division uses 10-digit precision. Division and modulo by zero return errors.

### Documentation

- **README overhaul** — added dataplane agent, WASM target, typed evaluation (generics), `IncludeResolver` interface, multi-error recovery, decimal arithmetic, IR constant folding, test framework, workflow and units packages to the architecture section. Updated status section with all v0.9.0–v0.12.0 features.

### Corrections

- gRPC expert sessions (StartSession, RunSession, AssertFacts, RetractFacts, GetSessionArbitrace, CloseSession) were already fully implemented in `grpcserver/expert.go`. All 22 proto methods are live. Previous evaluation incorrectly reported them as missing.

---

## v0.11.0

### Multi-Error Recovery

- **Lowering and validation now report all errors in one pass** — previously the compiler stopped at the first error. Now `ir.Lower` and `validateProgram` accumulate errors across declarations and return them all via `errors.Join`. The CLI outputs each diagnostic on its own line. The VS Code extension already parses multi-line diagnostics, so all errors now appear in the editor at once.

### WASM Target

- **`cmd/arbiter-wasm`** — new WASM build target. `GOOS=js GOARCH=wasm go build ./cmd/arbiter-wasm` produces a WASM module that exposes `arbiterCompile`, `arbiterEval`, `arbiterEvalGoverned`, and `arbiterEvalStrategy` to JavaScript. Includes `loader.js` for Node.js and browser environments. 29MB uncompressed / 17MB gzipped. WASM build added to CI.

### Include Resolver Interface

- **`IncludeResolver` interface** — include resolution is no longer hardcoded to the filesystem. `LoadFileUnitWithResolver(path, resolver)` accepts any implementation of `IncludeResolver`, which takes an include path and base directory and returns source bytes + resolved path. `DefaultResolver()` returns the filesystem resolver. This enables HTTP, registry, or in-memory include resolution.

---

## v0.10.0

### Concurrency Safety

- **StringPool data race fix** — `NewStringPool` now copies the backing array from the constant pool instead of sharing it. Concurrent evaluations against the same compiled ruleset were mutating a shared slice via `Intern()`. Added `sync.RWMutex` for thread-safe read/write access. This was a real production bug — found by new race-detector-verified concurrency tests.
- **Concurrent evaluation tests** — parallel `Eval`, `EvalGoverned`, and `EvalStrategy` tests spin up 100 goroutines against shared compiled rulesets. All pass under `-race`.

### Compiler

- **IR constant folding** — new optimization pass runs after validation, before bytecode compilation. Inlines `const` refs to their literal values, folds number arithmetic (`+`, `-`, `*`, `/`, `%`), short-circuits boolean ops (`false and X → false`, `true or X → true`), folds literal comparisons, and folds `not` on boolean literals. Division by zero is left for runtime.

### Validation

- **Rollout namespace collision detection** — compile-time validation now warns when two rollout clauses across rules, strategies, flags, or expert rules produce identical auto-derived namespaces, which would silently correlate independent rollouts.

### Testing

- **Fuzz targets** — `FuzzCompile`, `FuzzParse`, and `FuzzEval` for `go test -fuzz`. Exercises the parser with arbitrary bytes, the compiler with arbitrary `.arb` input, and the evaluator with arbitrary JSON contexts. No panics found.
- **Rollout distribution validation** — chi-squared uniformity test across 100K subjects proves bucket fairness. Per-percentage accuracy tests at 1%, 5%, 10%, 25%, 50%, 75%, 90%, 99%. Namespace independence and determinism verified.

---

## v0.9.0

### Testing

- **Strategy assertions in `.test.arb`** — `expect strategy X selected Y { field: value }` tests that a strategy selects the expected candidate with the expected params. Covers candidate selection, else-arm fallthrough, and param matching.

### Documentation

- **Grammar EBNF update** — added fact/outcome/feature/worker declarations, schema types, expert temporal clauses, join expressions, decimal/quantity/timestamp literals, and the full unit table.
- **AGENTS.md rewrite** — project context, build commands, commit rules, deploy info, and key directories.
- **`arbiter test` framing** — CLI help and changelog now describe testing in plain language instead of "executable bundle specs."

---

## v0.8.0

### Workers

- **Worker runtime integration** — workers now execute through a dedicated `WorkerHandler` surface instead of only piggybacking on generic delivery handlers, which gives the runtime a typed capability boundary for execution plus result handling.
- **`worker://` feedback path** — successful worker results are now materialized into runtime-owned `worker://name` sources on the next tick, so arbiters can reason about worker outputs without collapsing into an imperative in-tick sublanguage.
- **Typed worker output enforcement** — worker executions now validate returned fact/outcome shapes against the declared worker contract, and unknown `worker://...` source references are rejected at compile time.

### Tooling

- **Explore and CLI visibility** — `arbiter explore` summaries and compile/check surfaces now expose worker and arbiter declarations directly instead of hiding them behind the raw IR.
- **Editor highlighting** — VS Code and tree-sitter highlighting now recognize `worker`, `arbiter`, runtime handler keywords, and worker contract clauses.

### Release

- **Version bump to `0.8.0`** — SDK, editor package, and release metadata now align on `0.8.0` for the fully integrated worker runtime release.

---

## v0.7.0

### Runtime Surface

- **Strategy productization** — strategies now have a public gRPC evaluation path, runtime override controls, audit visibility, bundle metadata, and dataplane propagation instead of living only as an in-process helper.
- **Strategy override controls** — candidates can now be kill-switched or rollout-gated at runtime without source redeploys, which brings strategy in line with the existing operational model for rules and flags.

### Workflow And Language

- **`worker` primitive** — Arbiter now supports named worker declarations with typed `input` and `output` contracts so arbiters can invoke reusable capabilities without turning handlers into anonymous one-off targets.
- **Typed worker dispatch** — workflow validation and delivery now understand worker references, which gives arbiters a clearer capability layer while keeping decision semantics in the arbiter runtime.

### Tooling And Safety

- **Safer `gts` usage in-repo** — `scripts/gts-safe` serializes and bounds code-intelligence commands to avoid runaway background indexing and unsafe write-capable modes during local investigation.
- **Version bump to `0.7.0`** — SDK, editor package, and release metadata now align on `0.7.0` for the strategy-runtime and worker-capability release.

---

## v0.6.0

### Strategy

- **Native `strategy` primitive** — Arbiter now supports `strategy` declarations for deterministic, stateless governed routing over recognized decision shapes in current facts and state.
- **Recognition plus selection semantics** — strategies recognize one of a closed set of declared state shapes, require an explicit fallback, and select exactly one governed path with typed results and explainable arbitraces.
- **Shared runtime, not a parallel VM** — strategy candidates compile into synthetic governed rulesets so the primitive reuses the existing compiler, governance machinery, and evaluation runtime instead of introducing a separate execution model.

### Language And Tooling

- **End-to-end language support** — grammar, lowering, validation, syntax highlighting, and package APIs now understand strategies as a first-class language feature.
- **Shared compile/eval surface** — strategy compilation is now integrated into the common compile path, with root helpers for loading and evaluating compiled strategies alongside the rest of the bundle.
- **CLI and introspection support** — `arbiter strategy` evaluates a named strategy directly, and `arbiter explore` summaries now include strategy declarations and candidate structure.
- **Semantics hardening** — validation and tests now lock in required `else` behavior, duplicate-label rejection, kill-switch handling, malformed `else` defense-in-depth checks, and stable arbitrace structure.

### Product Direction

- **`emit` package removed** — the Rego, CEL, and Drools emitters are gone, and the `arbiter emit` CLI path has been removed to keep the system focused on native Arbiter execution rather than downstream emitter maintenance.
- **Version bump to `0.6.0`** — the published SDK and editor package versions now align on `0.6.0` for the strategy release.

---

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

- **`arbiter test` / `arbtest`** — write `.test.arb` files next to your bundles to test rules, flags, expert scenarios, and streamed arbiter scenarios. Run from the CLI or the `arbtest` Go package.
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
- **Deploy script** — `scripts/deploy.sh` with pre-flight postgres checks.

### Examples

- **Greenhouse plant management** — 17 expert rules demonstrating sensor-driven inference with soil moisture, nutrition, humidity, temperature, CO2 monitoring, two-phase gating for all-clear detection, and `excludes` for conditional action suppression.
- **LaunchDarkly-equivalent flag suite** — 7 flags across 9 segments covering boolean flags, multivariate flags, progressive rollouts, prerequisites, kill switches, variant payloads, segment+inline combos, runtime overrides, explain arbitraces, and edge cases. 30 test scenarios.
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
- Kill switches, prerequisites with cycle detection, explainability arbitraces.
- Runtime overrides for kill switches and rollout percentages without recompiling.

### Flags

- Boolean and multivariate flags with typed variant payloads.
- Schema validation at load time (type consistency across variants).
- Secret references (`secret("ref")`) preserved for core eval, redacted in explain/HTTP.
- Hot reload via `fsnotify` file watcher across the include graph.
- HTTP handler serving `/flags` and `/explain` endpoints.
- `LoadEnv(dir, env)` for per-environment flag files.

### Serving

- gRPC API: `PublishBundle`, `ListBundles`, `ActivateBundle`, `RollbackBundle`, `EvaluateRules`, `ResolveFlag`, `StartSession`, `RunSession`, `AssertFacts`, `RetractFacts`, `GetSessionArbitrace`, `CloseSession`, `SetRuleOverride`, `SetFlagOverride`, `SetFlagRuleOverride`.
- Bundle versioning with per-name history, activation, and rollback. SHA256 checksums. File-backed persistence.
- Session store with 30-minute TTL, LRU eviction at 10K sessions, per-session mutexes.
- Audit sink interface with JSONL default. Every decision logged with full context, arbitrace, and timestamps.

### Transpilation

- Emit to Rego (OPA), CEL, and Drools DRL with target-idiomatic output.
- Decompile bytecode back to Arishem JSON.
- Arishem JSON import via `CompileJSONRules` for migration.

### Authorization

- Thin ABAC helper: `authz.EvaluateSource(source, Request{Actor, Action, Resource})` standardizes context and checks for `Allow` actions.

### CLI

- `arbiter compile`, `arbiter eval`, `arbiter check`, `arbiter expert`, `arbiter serve`, `arbiter import`.
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
