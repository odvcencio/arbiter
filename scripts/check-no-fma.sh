#!/usr/bin/env bash
# Fails when the arm64 build contains a fused multiply-add in an Arbiter
# function. Go may fuse x*y + z into FMADD on arm64 but never does on amd64 or
# wasm, so a fused site gives different bits on arm64. Fix a hit by writing
# float64(x*y) + z. Standard-library and dependency functions are not checked.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d "${TMPDIR:-/tmp}/arbiter-fma.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
# Allowlist: the gRPC rate limiter refills a token bucket from wall-clock time;
# it never feeds rule evaluation.
allow='m31labs.dev/arbiter/grpcserver.\(\*tokenBucketLimiter\).take'
for pkg in ./cmd/arbiter ./cmd/arbiter-parity ./cmd/arbiter-runtime ./cmd/arbiter-agent ./cmd/arbiter-lsp; do
    [[ -d $pkg ]] || continue
    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -p 4 -o "$tmp/bin" "$pkg"
    go tool objdump "$tmp/bin" | awk '/^TEXT /{fn=$2} /[ \t](FMADD|FMSUB|FNMADD|FNMSUB)[DS]?[ \t]/{print fn}' | sort -u >> "$tmp/hits"
done
touch "$tmp/hits"
bad=$(sort -u "$tmp/hits" | grep '^m31labs.dev/arbiter' | grep -v -E "$allow" || true)
if [[ -n $bad ]]; then
    echo "fused multiply-add found in Arbiter functions (arm64):" >&2
    echo "$bad" >&2
    exit 1
fi
echo "fma-check: no fused multiply-add in Arbiter functions"
