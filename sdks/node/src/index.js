const path = require("node:path");
const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");
const googleProtoFiles = require("google-proto-files");

const protoPath = path.join(__dirname, "..", "proto", "arbiter", "v2", "service.proto");
const packageDefinition = protoLoader.loadSync(protoPath, {
  keepCase: false,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
  json: true,
  includeDirs: [
    path.join(__dirname, "..", "proto"),
    googleProtoFiles.getProtoPath(),
  ],
});
const proto = grpc.loadPackageDefinition(packageDefinition).arbiter.v1;

function unary(client, method, request) {
  return new Promise((resolve, reject) => {
    client[method](request, (err, response) => {
      if (err) {
        reject(err);
        return;
      }
      resolve(response);
    });
  });
}

class ArbiterClient {
  constructor(target, credentials = grpc.credentials.createInsecure()) {
    this.client = new proto.ArbiterService(target, credentials);
  }

  close() {
    this.client.close();
  }

  publishBundle({ name, source }) {
    return unary(this.client, "PublishBundle", {
      name,
      source: Buffer.isBuffer(source) ? source : Buffer.from(source),
    });
  }

  listBundles({ name = "" } = {}) {
    return unary(this.client, "ListBundles", { name });
  }

  activateBundle({ name, bundleId }) {
    return unary(this.client, "ActivateBundle", { name, bundleId });
  }

  rollbackBundle({ name }) {
    return unary(this.client, "RollbackBundle", { name });
  }

  getBundle({ bundleId = "", bundleName = "" } = {}) {
    return unary(this.client, "GetBundle", { bundleId, bundleName });
  }

  watchBundles({ names = [], activeOnly = false } = {}) {
    return this.client.WatchBundles({ names, activeOnly });
  }

  getOverrides({ bundleId = "", bundleName = "" } = {}) {
    return unary(this.client, "GetOverrides", { bundleId, bundleName });
  }

  watchOverrides({ bundleId }) {
    return this.client.WatchOverrides({ bundleId });
  }

  evaluateRules({ bundleId = "", bundleName = "", context = {}, requestId = "" }) {
    return unary(this.client, "EvaluateRules", {
      bundleId,
      bundleName,
      context,
      requestId,
    });
  }

  resolveFlag({ bundleId = "", bundleName = "", flagKey, context = {}, requestId = "" }) {
    return unary(this.client, "ResolveFlag", {
      bundleId,
      bundleName,
      flagKey,
      context,
      requestId,
    });
  }

  startSession({ bundleId = "", bundleName = "", envelope = {}, facts = [] }) {
    return unary(this.client, "StartSession", {
      bundleId,
      bundleName,
      envelope,
      facts,
    });
  }

  runSession({ sessionId, requestId = "" }) {
    return unary(this.client, "RunSession", { sessionId, requestId });
  }

  assertFacts({ sessionId, facts }) {
    return unary(this.client, "AssertFacts", { sessionId, facts });
  }

  retractFacts({ sessionId, facts }) {
    return unary(this.client, "RetractFacts", { sessionId, facts });
  }

  getSessionTrace({ sessionId }) {
    return unary(this.client, "GetSessionTrace", { sessionId });
  }

  closeSession({ sessionId }) {
    return unary(this.client, "CloseSession", { sessionId });
  }

  setRuleOverride({ bundleId, ruleName, killSwitch = undefined, rollout = undefined }) {
    return unary(this.client, "SetRuleOverride", {
      bundleId,
      ruleName,
      killSwitch: killSwitch === undefined ? undefined : { value: killSwitch },
      rollout: rollout === undefined ? undefined : { value: rollout },
    });
  }

  setFlagOverride({ bundleId, flagKey, killSwitch = undefined }) {
    return unary(this.client, "SetFlagOverride", {
      bundleId,
      flagKey,
      killSwitch: killSwitch === undefined ? undefined : { value: killSwitch },
    });
  }

  setFlagRuleOverride({ bundleId, flagKey, ruleIndex, rollout = undefined }) {
    return unary(this.client, "SetFlagRuleOverride", {
      bundleId,
      flagKey,
      ruleIndex,
      rollout: rollout === undefined ? undefined : { value: rollout },
    });
  }
}

module.exports = {
  ArbiterClient,
};
