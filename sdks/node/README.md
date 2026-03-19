# Arbiter Node SDK

Thin Node client for the Arbiter gRPC API.

## Install

```bash
npm install
```

## Example

```js
const { ArbiterClient } = require("./src");

async function main() {
  const client = new ArbiterClient("127.0.0.1:8081");
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

See [src/index.js](/home/draco/work/arbiter/sdks/node/src/index.js) and [examples/smoke.js](/home/draco/work/arbiter/sdks/node/examples/smoke.js).
