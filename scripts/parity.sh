#!/usr/bin/env bash
# Cross-target determinism check. Builds cmd/arbiter-parity for each target,
# runs it, and fails unless every target prints the same digests.
#
# Targets: amd64 GOAMD64=v1 (20 repeated runs), amd64 GOAMD64=v3, js/wasm under
# Node, and native arm64. For arm64 set PARITY_ARM64_HOST to an ssh host that is
# a native arm64 macOS machine (all remote files live in one mktemp directory under /tmp/arbiter-parity.*,
# which is removed afterwards, and the binary runs at background priority with
# GOMAXPROCS=2). Without it, qemu-aarch64 is used when installed.
#
# Environment:
#   PARITY_OUTPUT_DIR  where binaries and per-target output go (default: mktemp)
#   PARITY_RUNS        repeated amd64 v1 runs that must match (default 20)
#   PARITY_ARM64_HOST  ssh host for native arm64 (optional)
set -euo pipefail
cd "$(dirname "$0")/.."

out=${PARITY_OUTPUT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/arbiter-parity.XXXXXX")}
runs=${PARITY_RUNS:-20}
mkdir -p "$out"
command -v node >/dev/null 2>&1 || { echo 'Node.js must be on PATH for wasm verification' >&2; exit 1; }
echo "output directory: $out"
scripts/check-no-fma.sh

GOOS=linux GOARCH=amd64 GOAMD64=v1 go build -p 4 -o "$out/parity-v1" ./cmd/arbiter-parity
for i in $(seq 1 "$runs"); do
    "$out/parity-v1" > "$out/run-$i.txt"
    cmp -s "$out/run-1.txt" "$out/run-$i.txt" || { echo "amd64 v1 run $i differs from run 1" >&2; exit 1; }
done
cp "$out/run-1.txt" "$out/amd64-v1.txt"
"$out/parity-v1" -dump "$out/amd64-v1.dump" > /dev/null

GOOS=linux GOARCH=amd64 GOAMD64=v3 go build -p 4 -o "$out/parity-v3" ./cmd/arbiter-parity
"$out/parity-v3" -dump "$out/amd64-v3.dump" > "$out/amd64-v3.txt"

GOOS=js GOARCH=wasm go build -p 4 -o "$out/parity.wasm" ./cmd/arbiter-parity
"$(go env GOROOT)/lib/wasm/go_js_wasm_exec" "$out/parity.wasm" -dump "$out/wasm.dump" > "$out/wasm.txt"

if [[ -n ${PARITY_ARM64_HOST:-} ]]; then
    host=$PARITY_ARM64_HOST
    platform=$(ssh -o BatchMode=yes -o ConnectTimeout=5 "$host" 'uname -sm') || { echo "cannot reach PARITY_ARM64_HOST=$host over ssh" >&2; exit 1; }
    [[ $platform == 'Darwin arm64' ]] || { echo "PARITY_ARM64_HOST=$host is '$platform', need a native 'Darwin arm64' machine" >&2; exit 1; }
    remote=$(ssh -o BatchMode=yes "$host" 'mktemp -d /tmp/arbiter-parity.XXXXXX')
    [[ $remote == /tmp/arbiter-parity.* ]]
    cleanup() { ssh -o BatchMode=yes "$host" "rm -rf '$remote'"; }
    trap cleanup EXIT
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -p 4 -o "$out/parity-arm64" ./cmd/arbiter-parity
    scp -o BatchMode=yes -q "$out/parity-arm64" "$host:$remote/parity"
    ssh -o BatchMode=yes "$host" "chmod 700 '$remote/parity'; GOMAXPROCS=2 taskpolicy -b '$remote/parity' -dump '$remote/arm64.dump'" > "$out/arm64.txt"
    scp -o BatchMode=yes -q "$host:$remote/arm64.dump" "$out/arm64.dump"
    cleanup
    trap - EXIT
elif command -v qemu-aarch64 >/dev/null 2>&1; then
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -p 4 -o "$out/parity-arm64" ./cmd/arbiter-parity
    qemu-aarch64 "$out/parity-arm64" -dump "$out/arm64.dump" > "$out/arm64.txt"
else
    echo 'arm64 evidence is required: set PARITY_ARM64_HOST or install qemu-user' >&2
    exit 1
fi

status=0
for target in amd64-v3 wasm arm64; do
    if ! cmp -s "$out/amd64-v1.txt" "$out/$target.txt"; then
        echo "MISMATCH: $target differs from amd64-v1" >&2
        diff "$out/amd64-v1.txt" "$out/$target.txt" >&2 || true
        echo "first differing corpus lines (dump diff):" >&2
        diff "$out/amd64-v1.dump" "$out/$target.dump" 2>&1 | head -40 >&2 || true
        status=1
    fi
done
printf '%-10s %s\n' amd64-v1 "$(tail -1 "$out/amd64-v1.txt" | cut -d' ' -f2)" \
    amd64-v3 "$(tail -1 "$out/amd64-v3.txt" | cut -d' ' -f2)" \
    wasm "$(tail -1 "$out/wasm.txt" | cut -d' ' -f2)" \
    arm64 "$(tail -1 "$out/arm64.txt" | cut -d' ' -f2)"
[[ $status -eq 0 ]] && echo "parity: all targets match"
exit $status
