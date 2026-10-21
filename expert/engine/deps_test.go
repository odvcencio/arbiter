package engine

import (
	"os/exec"
	"strings"
	"testing"
)

// The engine exists so that programs loading precompiled rules do not link the
// .arb parser or the tree-sitter grammars. Fail if a dependency brings them back.
func TestEngineDoesNotImportParser(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available")
	}
	out, err := exec.Command(goBin, "list", "-deps", "m31labs.dev/arbiter/expert/engine").Output()
	if err != nil {
		t.Skipf("go list failed: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		switch {
		case dep == "m31labs.dev/arbiter",
			dep == "m31labs.dev/arbiter/grammar",
			strings.HasPrefix(dep, "m31labs.dev/arbiter/grammar/"),
			strings.HasPrefix(dep, "github.com/odvcencio/gotreesitter/grammars"),
			strings.HasPrefix(dep, "github.com/odvcencio/gotreesitter/grammargen"):
			t.Errorf("engine imports %s", dep)
		}
	}
}
