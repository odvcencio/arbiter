# Arbiter Rust SDK

Rust client crate for the Arbiter gRPC API with bearer-token metadata and bounded retries on transient unary failures.

## Build

```bash
cargo build
```

## Example

```bash
cargo run --example smoke
```

The client accepts bare `host:port` targets for local plaintext use and `https://...` targets for TLS endpoints. Add a token with `.with_token("...")` when the server requires auth.

The runtime control surface is separate from the bundle/eval API. Use
`RuntimeClient` to inspect one `arbiter-runtime` instance:

```rust
use arbiter_sdk::RuntimeClient;

# async fn run() -> Result<(), Box<dyn std::error::Error>> {
let runtime = RuntimeClient::connect("127.0.0.1:7081").await?;
let caps = runtime.get_runtime_capabilities().await?;
println!("{}", caps.sources.len());
# Ok(())
# }
```

Use `ControlClient` to inspect one hosted `arbiter serve` control plane:

```rust
use arbiter_sdk::ControlClient;

# async fn run() -> Result<(), Box<dyn std::error::Error>> {
let control = ControlClient::connect("127.0.0.1:8081").await?;
let status = control.get_control_status().await?;
println!("{}", status.bundles.unwrap().active_total);
# Ok(())
# }
```

## Capability Plugins

The crate also ships a capability-service helper for non-Go runtime plugins. Implement the handler traits, register them on a `CapabilityPlugin`, and hand the resulting service to tonic:

```rust
use arbiter_sdk::{CapabilityPlugin, SinkHandler};
use arbiter_sdk::arbiter::v1::capability_service_server::CapabilityServiceServer;
use tonic::{async_trait, transport::Server, Status};

#[derive(Default)]
struct DiscordSink;

#[async_trait]
impl SinkHandler for DiscordSink {
    async fn deliver_outcome(
        &self,
        delivery: arbiter_sdk::arbiter::v1::DeliveryContext,
    ) -> Result<(), Status> {
        println!("deliver {} to {}", delivery.outcome.unwrap().name, delivery.handler_target);
        Ok(())
    }
}

# async fn run() -> Result<(), Box<dyn std::error::Error>> {
let mut plugin = CapabilityPlugin::new("ops-plugin").with_version("1.0.0");
plugin.register_sink("discord", "post governed outcomes to discord", DiscordSink::default())?;

Server::builder()
    .add_service(plugin.into_service())
    .serve("127.0.0.1:7090".parse()?)
    .await?;
# Ok(())
# }
```

See [src/lib.rs](src/lib.rs) and [examples/smoke.rs](examples/smoke.rs).
