# SDKs

Thin Arbiter clients live here, generated or packaged off the gRPC API in
[service.proto](/home/draco/work/arbiter/proto/arbiter/v1/service.proto).

Current SDKs:

- `python/` — generated protobuf/grpc client plus a small convenience wrapper
- `node/` — thin `@grpc/grpc-js` client with runtime proto loading
- `rust/` — `tonic` client crate with `build.rs` proto compilation

All three target the same control-plane surface:

- bundle publish, list, activation, rollback
- rule evaluation and flag resolution
- expert session lifecycle
- runtime override mutation

Java is still pending. There is no JDK/Maven toolchain in this environment, so
it was not added as an unverified skeleton.
