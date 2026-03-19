from arbiter_sdk import ArbiterClient

SOURCE = """
rule Approve {
    when { user.score >= 700 }
    then Approved { tier: "gold" }
}
"""


def main() -> None:
    with ArbiterClient("127.0.0.1:18081") as client:
        publish = client.publish_bundle("python-smoke", SOURCE)
        result = client.evaluate_rules(
            bundle_name="python-smoke",
            context={"user": {"score": 720}},
            request_id="py-smoke",
        )
        if len(result.matched) != 1 or result.matched[0].action != "Approved":
            raise SystemExit(f"unexpected evaluate response: {result}")
        print(f"python sdk ok: {publish.bundle_id}")
