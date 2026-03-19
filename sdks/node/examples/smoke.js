const { ArbiterClient } = require("../src");

const source = `
rule Approve {
  when { user.score >= 700 }
  then Approved { tier: "gold" }
}
`;

async function main() {
  const client = new ArbiterClient("127.0.0.1:18081");
  try {
    const publish = await client.publishBundle({ name: "node-smoke", source });
    const result = await client.evaluateRules({
      bundleName: "node-smoke",
      context: { user: { score: 720 } },
      requestId: "node-smoke",
    });
    if (!result.matched || result.matched.length !== 1 || result.matched[0].action !== "Approved") {
      throw new Error(`unexpected evaluate response: ${JSON.stringify(result)}`);
    }
    console.log(`node sdk ok: ${publish.bundleId}`);
  } finally {
    client.close();
  }
}

main().catch(err => {
  console.error(err);
  process.exit(1);
});
