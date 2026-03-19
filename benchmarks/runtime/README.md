# Runtime Benchmarks

Cross-engine comparison benchmarks live in this module so CEL and OPA dependencies do not affect the main Arbiter module.

Run from this directory:

```bash
go test -run '^$' -bench BenchmarkCompareArbiterCELOPA -benchmem
```
