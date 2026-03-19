use arbiter_sdk::ArbiterClient;
use serde_json::json;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut client = ArbiterClient::connect("http://127.0.0.1:18081").await?;
    let publish = client
        .publish_bundle(
            "rust-smoke",
            r#"
rule Approve {
    when { user.score >= 700 }
    then Approved { tier: "gold" }
}
"#,
        )
        .await?;
    let result = client
        .evaluate_rules_by_name("rust-smoke", json!({"user": {"score": 720}}), "rust-smoke")
        .await?;
    if result.matched.len() != 1 || result.matched[0].action != "Approved" {
        return Err(format!("unexpected evaluate response: {:?}", result).into());
    }
    println!("rust sdk ok: {}", publish.bundle_id);
    Ok(())
}
