#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${ARBITER_SDK_SMOKE_PORT:-18081}"
TMPDIR="$(mktemp -d)"
cleanup() {
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "${SERVER_PID}" >/dev/null 2>&1 || true
    wait "${SERVER_PID}" >/dev/null 2>&1 || true
  fi
  rm -rf "${TMPDIR}"
}
trap cleanup EXIT

cd "${ROOT}"
go run ./cmd/arbiter serve \
  --grpc "127.0.0.1:${PORT}" \
  --bundle-file "${TMPDIR}/bundles.json" \
  --overrides-file "${TMPDIR}/overrides.json" \
  --audit-file "${TMPDIR}/audit.jsonl" \
  >"${TMPDIR}/server.stdout" 2>"${TMPDIR}/server.stderr" &
SERVER_PID=$!

for _ in $(seq 1 50); do
  if grep -q "listening" "${TMPDIR}/server.stderr" 2>/dev/null; then
    break
  fi
  sleep 0.2
done

export PYTHONPATH="${ROOT}/sdks/python/src"
python3 "${ROOT}/sdks/python/examples/smoke.py"

pushd "${ROOT}/sdks/node" >/dev/null
npm install --silent
node examples/smoke.js
popd >/dev/null

pushd "${ROOT}/sdks/rust" >/dev/null
cargo run --quiet --example smoke
popd >/dev/null

echo "sdk smoke ok"
