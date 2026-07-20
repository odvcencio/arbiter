# Arbiter Agent Instructions

## What This Is

Arbiter is a compact language for governed outcomes. The compiler (this repo) is written in Go. It compiles `.arb` files to bytecode and evaluates them via a VM. Four evaluation modes: stateless eval, feature flags, expert inference (forward-chaining), and continuous arbiters (always-on workflows). Module path: `github.com/odvcencio/arbiter`.

For agents helping someone use Arbiter as a dependency, read the canonical skill first: [using-arbiter](https://github.com/odvcencio/m31labs-skills/blob/main/skills/using-arbiter/SKILL.md).

## Build & Test

```bash
go test ./... -count=1 -short -timeout=120s   # run tests
go vet ./...                                    # lint
go build ./cmd/arbiter                          # build CLI
go build ./cmd/arbiter-agent                    # build agent
go run ./cmd/arbiter check testdata/fraud.arb   # validate a rule file
```

## Commits

- Use `buckley commit --yes -min -graft` — not `git commit`.
- No Co-Authored-By lines.
- Never commit spec docs, plans, or design documents.
- Entity extraction always on (never `--skip-entities`).

## Agent Coordination

- Register with a tree species name: `graft workon --as "{name}"` (birch, cedar, maple, oak, etc.)
- Run `graft coord check` before committing.
- Sign off when done: `graft workon --done --as "{name}"`

## Deploy

- Dockerfile: `deploy/Dockerfile` — multi-stage Alpine build, entry point `arbiter serve --grpc :8081`
- K8s manifests: `deploy/k8s.yaml` — Harbor registry at `harbor.draco.quest/orchard/arbiter:latest`
- Use `kubectl`, not docker compose or Helm.

## Key Directories

| Path | Purpose |
|------|---------|
| `compiler/` | Bytecode compiler pipeline |
| `vm/` | Virtual machine execution |
| `ir/` | Intermediate representation |
| `expert/` | Forward-chaining expert inference |
| `workflow/` | Continuous arbiter runtime |
| `govern/` | Kill switches, rollouts, prerequisites |
| `strategy/` | Strategy declaration evaluation |
| `flags/` | Feature flag resolution |
| `dataplane/` | Data plane and fact sources |
| `cmd/arbiter/` | CLI (check, compile, eval, diff, replay, serve, etc.) |
| `sdks/` | Node, Python, Rust client SDKs |
| `examples/` | Example `.arb` files |

## Prose: ASD-STE100

Write agent prose in ASD-STE100 style (decision 0011, hypha://m31labs/hyphae). Three rules
matter most: use the active voice; keep sentences at or below 20-25 words; give each word one
meaning. This rule covers commit messages, PR text, review output, and documentation.
