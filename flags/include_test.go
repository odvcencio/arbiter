package flags

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileResolvesIncludes(t *testing.T) {
	dir := t.TempDir()
	writeFlagTestFile(t, dir, "segments.arb", `
segment enterprise {
	user.plan == "enterprise"
}
`)
	writeFlagTestFile(t, dir, "flags.arb", `
flag checkout_v2 type boolean default false {
	when enterprise then true
}
`)
	main := writeFlagTestFile(t, dir, "main.arb", `
include "segments.arb"
include "flags.arb"
`)

	f, err := LoadFile(main)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	got := f.VariantName("checkout_v2", map[string]any{
		"user": map[string]any{"plan": "enterprise"},
	})
	if got != "true" {
		t.Fatalf("expected include-backed flag resolution, got %q", got)
	}
}

func writeFlagTestFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
