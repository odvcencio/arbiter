# Arbiter Node SDK

Two ways to evaluate:

- **gRPC client** (`@arbiter/sdk-node`) — talks to a hosted control plane.
- **Local eval** (`@arbiter/sdk-node/local`) — evaluates compiled `.arb` rules
  in-process via WebAssembly, no server round-trip.

The gRPC client carries bearer-token metadata and bounded retries on transient unary failures.

## Local evaluation (in-process, no server)

```bash
npm run build:wasm   # builds wasm/{arbiter.wasm,wasm_exec.js,loader.js}
```

```js
const arbiter = require("@arbiter/sdk-node/local");

async function main() {
  await arbiter.init();
  arbiter.compile(`rule BigOrder { when { total >= 100 } then Flag { tier: "vip" } }`);
  const matched = arbiter.evalGoverned(JSON.stringify({ total: 250 }));
  console.log(matched); // [{ name: "BigOrder", action: "Flag", params: { tier: "vip" } }]
}
```

## Install

```bash
npm install
```

## Example

```js
const { ArbiterClient } = require("./src");

async function main() {
  const client = new ArbiterClient("http://127.0.0.1:8081");
  const publish = await client.publishBundle({
    name: "checkout",
    source: 'rule Approve { when { true } then Ok {} }',
  });
  const result = await client.evaluateRules({
    bundleName: "checkout",
    context: { user: { score: 720 } },
  });
  console.log(publish.bundleId, result.matched.length);
  client.close();
}
```

For a managed or TLS-terminated endpoint, pass an `https://` target and a token:

```js
const client = new ArbiterClient("https://arbiter.internal:443", {
  token: process.env.ARBITER_TOKEN,
});
```

The runtime control surface is separate from the bundle/eval API. Use `RuntimeClient`
to inspect one `arbiter-runtime` instance:

```js
const { RuntimeClient } = require("./src");

async function main() {
  const runtime = new RuntimeClient("http://127.0.0.1:7081");
  const caps = await runtime.getRuntimeCapabilities();
  console.log(caps.sources.map(item => `${item.scheme}:${item.owner}`));
  runtime.close();
}
```

Use `ControlClient` to inspect one hosted `arbiter serve` control plane:

```js
const { ControlClient } = require("./src");

async function main() {
  const control = new ControlClient("http://127.0.0.1:8081");
  const status = await control.getControlStatus();
  console.log(status.bundles.activeTotal, status.sessions.active);
  control.close();
}
```

## Capability Plugins

```js
const grpc = require("@grpc/grpc-js");
const { CapabilityServer } = require("./src");

const plugin = new CapabilityServer({ name: "ops-plugin", version: "1.0.0" })
  .registerSource("kafka", target => [{
    type: "OrderEvent",
    key: "evt-1",
    fields: { topic: target, status: "new" },
  }], { description: "load facts from kafka topics" })
  .registerSink("discord", delivery => {
    console.log("deliver", delivery.outcome.name, "to", delivery.handler_target);
  }, { description: "post governed outcomes to discord" })
  .registerWorker("python", ({ worker, delivery }) => ({
    facts: [{
      type: worker.output,
      key: delivery.outcome.params.key,
      fields: { status: "sent" },
    }],
  }), { description: "delegate worker execution to python" });

plugin.listen("127.0.0.1:7090", grpc.ServerCredentials.createInsecure());
```

See [src/index.js](src/index.js) and [examples/smoke.js](examples/smoke.js).
