# Compiler Architecture

This document explains the Arbiter compiler as it exists in the OSS engine. It
is meant for contributors working in `compiler/`, `grammar.go`, or `vm/`.

## Pipeline

Arbiter compilation is deliberately split into a few simple phases:

1. Parse `.arb` source with the tree-sitter grammar in [`grammar.go`](../../grammar.go).
2. Walk the CST in [`compiler/compiler.go`](../../compiler/compiler.go).
3. Emit a [`CompiledRuleset`](../../compiler/ruleset.go):
   - rule headers
   - action tables
   - prereq/exclude tables
   - bytecode instructions
   - interned constants
4. Evaluate the ruleset in the VM under [`vm/`](../../vm).

The compiler is intentionally CST-driven rather than AST-driven. The tree-sitter
node shape is stable enough for the language today, and staying close to the CST
keeps transpilers and the bytecode compiler aligned.

## Key Files

- [`grammar.go`](../../grammar.go): executable grammar definition
- [`compiler/compiler.go`](../../compiler/compiler.go): CST walk and bytecode emission
- [`compiler/ruleset.go`](../../compiler/ruleset.go): compiled metadata layout
- [`compiler/opcode.go`](../../compiler/opcode.go): VM instruction set
- [`vm/vm.go`](../../vm/vm.go): bytecode evaluation
- [`expert/`](../../expert): forward-chaining runtime built on top of the same language

## What the Compiler Emits

The compiler does not emit one giant opaque blob. The important outputs are:

- `Rules`: metadata per rule, including governance fields like kill switch,
  rollout, prereqs, excludes, and optional segment references
- `Actions`: structured outcome payload instructions
- `Instructions`: bytecode for rule conditions and parameter expressions
- `Constants`: the compile-time constant pool
- `Prereqs` / `Excludes`: flattened tables referenced by offsets in each rule

That layout is why small targeted refactors in `compileRule` matter: the rule
header is the language contract between parsing and runtime governance.

## The Two-Pool String Story

There are two related, but distinct, string pools:

### Compile-Time Pool

[`intern.Pool`](../../intern/pool.go) is owned by the compiled ruleset. It
interns:

- rule names
- field names
- string literals
- rollout namespaces
- action names

This keeps compiled bundles compact and avoids repeating identical strings
across thousands of rules.

### Runtime Pool

[`vm.StringPool`](../../vm/datacontext.go) starts from the compiled string table
and may grow at evaluation time for data values that only exist in live input.

Why both exist:

- the compiler needs stable indexes in the compiled artifact
- the runtime needs fast lookup for request/session data that was not known at
  compile time

The split is correct, but it is a contributor hotspot because it means:

- compile-time metadata and runtime data do not live in exactly the same table
- new compiler features must be explicit about which pool they are targeting
- cross-package changes often touch both `compiler/` and `vm/`

When working in this area, prefer small helpers that make the intent explicit.
For example: rule metadata population, governance table collection, condition
emission, and rollout parsing should remain separate concerns.

## Compiler Maintenance Rules

These are the practical rules worth following when changing compilation logic:

1. Keep `compileRule` orchestration shallow.
   - Header fields, governance tables, condition emission, actions, and rollout
     parsing should live in focused helpers.
2. Add metadata tests whenever a rule header field changes.
   - Governance regressions are easy to miss because they often compile
     successfully but change runtime behavior.
3. Prefer additive tables plus offsets over nested per-rule allocations.
   - That keeps memory predictable and the runtime cache-friendly.
4. Keep the standalone grammar spec and executable grammar in sync.
   - If `grammar.go` changes materially, update `docs/language/grammar.ebnf`.
5. Treat README examples as product-facing, not exhaustive.
   - Contributor details belong in docs like this one and in focused tests.

## Known Pressure Points

The main maintainability hotspot remains [`compiler/compiler.go`](../../compiler/compiler.go).
That file still centralizes a lot of CST lowering logic even after helper
extraction. It is the right place to keep emission code, but the long-term path
is more decomposition by concern:

- rule/flag/expert declaration lowering
- expression lowering
- action lowering
- governance lowering

The goal is not “many tiny files” for its own sake. The goal is to keep the
language contract obvious enough that a contributor can safely add one feature
without re-deriving the entire compiler in their head.
