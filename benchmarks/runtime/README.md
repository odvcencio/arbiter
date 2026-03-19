# Runtime Benchmarks

Cross-engine comparison benchmarks live in this module so CEL and OPA dependencies do not affect the main Arbiter module.

Run from this directory:

```bash
go test -run '^$' -bench '^BenchmarkCompare' -benchmem
```

Current compare benches cover:

- simple hot-path predicate eval across Arbiter, CEL, and OPA
- nested document traversal across Arbiter, CEL, and OPA
- compile / prepare cost across Arbiter, CEL, and OPA

The module also includes Arbiter-only workloads for governed rules, flags, expert sessions, and bundle compilation, kept here so the extra comparison dependencies stay quarantined from the main module.
