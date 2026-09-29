# Arbiter Python SDK

Python client for the Arbiter gRPC API with bearer-token metadata and bounded retries on transient unary failures.

## Install

```bash
pip install -e .
```

## Example

```python
from arbiter_sdk import ArbiterClient

with ArbiterClient("http://127.0.0.1:8081") as client:
    publish = client.publish_bundle("checkout", b'rule Approve { when { true } then Ok {} }')
    result = client.evaluate_rules(
        bundle_name="checkout",
        context={"user": {"score": 720}},
    )
    print(publish.bundle_id, len(result.matched))
```

For TLS endpoints, use an `https://` target and pass a token:

```python
with ArbiterClient("https://arbiter.internal:443", token="...", secure=True) as client:
    ...
```

The runtime control surface is separate from the bundle/eval API. Use
`RuntimeClient` to inspect one `arbiter-runtime` instance:

```python
from arbiter_sdk import RuntimeClient

with RuntimeClient("http://127.0.0.1:7081") as runtime:
    caps = runtime.get_runtime_capabilities()
    print([(item.scheme, item.owner) for item in caps.sources])
```

Use `ControlClient` to inspect one hosted `arbiter serve` control plane:

```python
from arbiter_sdk import ControlClient

with ControlClient("http://127.0.0.1:8081") as control:
    status = control.get_control_status()
    print(status.bundles.active_total, status.sessions.active)
```

## Capability Plugins

```python
from arbiter_sdk import CapabilityServer

plugin = CapabilityServer(name="ops-plugin", version="1.0.0")

plugin.register_source(
    "kafka",
    lambda target, _ctx: [{
        "type": "OrderEvent",
        "key": "evt-1",
        "fields": {"topic": target, "status": "new"},
    }],
    description="load facts from kafka topics",
)

plugin.register_sink(
    "discord",
    lambda delivery, _ctx: print("deliver", delivery["outcome"]["name"], "to", delivery["handler_target"]),
    description="post governed outcomes to discord",
)

plugin.register_worker(
    "python",
    lambda req, _ctx: {
        "facts": [{
            "type": req["worker"]["output"],
            "key": req["delivery"]["outcome"]["params"]["key"],
            "fields": {"status": "sent"},
        }]
    },
    description="delegate worker execution to python",
)

server = plugin.serve("127.0.0.1:7090")
server.wait_for_termination()
```

See [examples/smoke.py](examples/smoke.py) for a runnable end-to-end example.
