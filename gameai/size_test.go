package gameai_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// wasmSizeBudget caps the js/wasm size of the probe program, which loads a
// bundle, binds a view struct and makes one decision. Most of the probe is the Go standard library
// (fmt, encoding/json, regexp, math/big, crypto/sha256), which a game client
// usually links already. Raise the budget only with a reason.
//
// Measured with go1.26.10, -trimpath -ldflags=-s -w: 4,386,682 bytes raw,
// 916,260 bytes brotli -q 11. TinyGo 0.41.1 (-target wasm -no-debug) builds
// the same probe in 810,768 bytes raw, 244,822 brotli.
const wasmSizeBudget = 4_718_592 // 4.5 MiB

// TestWasmSize builds the probe for js/wasm and fails if it outgrows the
// budget. It also reports the brotli -q 11 size when brotli is installed.
func TestWasmSize(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a js/wasm binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available")
	}
	out := filepath.Join(t.TempDir(), "probe.wasm")
	cmd := exec.Command(goBin, "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./internal/wasmprobe")
	cmd.Env = append(cmd.Environ(), "GOOS=js", "GOARCH=wasm")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, msg)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("js/wasm probe: %d bytes raw", info.Size())
	if brotli, err := exec.LookPath("brotli"); err == nil {
		var compressed bytes.Buffer
		c := exec.Command(brotli, "-q", "11", "-c", out)
		c.Stdout = &compressed
		if err := c.Run(); err == nil {
			t.Logf("js/wasm probe: %d bytes brotli -q 11", compressed.Len())
		}
	}
	if info.Size() > wasmSizeBudget {
		t.Fatalf("js/wasm probe is %d bytes, over the %d byte budget", info.Size(), wasmSizeBudget)
	}
}
