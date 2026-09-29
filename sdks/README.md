# SDKs

Thin Arbiter clients live here, generated or packaged off the gRPC APIs in
[service.proto](../proto/arbiter/v1/service.proto) and
[capability.proto](../proto/arbiter/v1/capability.proto).

Current SDKs:

- `python/` — generated protobuf/grpc client plus a small convenience wrapper
- `node/` — thin `@grpc/grpc-js` client with runtime proto loading
- `rust/` — `tonic` client crate with `build.rs` proto compilation

All three target the same control-plane surface:

- bundle publish, list, activation, rollback
- bundle fetch and watch for local-agent sync
- override fetch and watch for local-agent sync
- rule evaluation, flag resolution, and strategy evaluation
- expert session lifecycle
- runtime override mutation, including strategy-candidate governance
- runtime capability introspection through `RuntimeService.GetRuntimeCapabilities`
- runtime status introspection through `RuntimeService.GetRuntimeStatus`
- status-issue catalog introspection through `GetStatusIssueCatalog`
- agent sync-status introspection through `AgentService.GetAgentStatus`
- hosted control-plane status introspection through `ControlService.GetControlStatus`

They also ship the capability-service contract so Node, Python, and Rust hosts
can implement remote source loaders, sink handlers, and worker runtimes for the
continuous-arbiter runner.

Each SDK now includes a helper layer for serving that contract, not just the raw
generated types:

- Node exports `CapabilityServer`
- Node exports `RuntimeClient`
- Node exports `AgentClient`
- Node exports `ControlClient`
- Python exports `CapabilityServer`
- Python exports `RuntimeClient`
- Python exports `AgentClient`
- Python exports `ControlClient`
- Rust exports `CapabilityPlugin` plus `SourceHandler` / `SinkHandler` / `WorkerHandler`
- Rust exports `RuntimeClient`
- Rust exports `AgentClient`
- Rust exports `ControlClient`

Java is still pending. There is no JDK/Maven toolchain in this environment, so
it was not added as an unverified skeleton.
