# Arbiter Python SDK

Thin Python client for the Arbiter gRPC API.

## Install

```bash
pip install -e .
```

## Example

```python
from arbiter_sdk import ArbiterClient

with ArbiterClient("127.0.0.1:8081") as client:
    publish = client.publish_bundle("checkout", b'rule Approve { when { true } then Ok {} }')
    result = client.evaluate_rules(
        bundle_name="checkout",
        context={"user": {"score": 720}},
    )
    print(publish.bundle_id, len(result.matched))
```

See [examples/smoke.py](/home/draco/work/arbiter/sdks/python/examples/smoke.py) for a runnable end-to-end example.
