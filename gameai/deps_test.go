package gameai_test

import (
	"os/exec"
	"strings"
	"testing"
)

// runtimePackages must build without the .arb parser, the IR, the compiler or
// gotreesitter, because a browser client links them into its js/wasm binary.
var runtimePackages = []string{
	"m31labs.dev/arbiter/gameai",
	"m31labs.dev/arbiter/vm",
	"m31labs.dev/arbiter/bundle/wire",
	"m31labs.dev/arbiter/bytecode",
}

func forbiddenRuntimeDep(dep string) bool {
	switch {
	case dep == "m31labs.dev/arbiter",
		dep == "m31labs.dev/arbiter/ir",
		dep == "m31labs.dev/arbiter/compiler",
		dep == "m31labs.dev/arbiter/decompile",
		dep == "m31labs.dev/arbiter/gostruct",
		dep == "m31labs.dev/arbiter/protoschema",
		dep == "m31labs.dev/arbiter/grammar",
		strings.HasPrefix(dep, "m31labs.dev/arbiter/grammar/"),
		dep == "github.com/odvcencio/gotreesitter",
		strings.HasPrefix(dep, "github.com/odvcencio/gotreesitter/"):
		return true
	}
	return false
}

// TestRuntimeDoesNotLinkParser fails if a change makes the runtime packages
// import the parser again, on the host target or on js/wasm.
func TestRuntimeDoesNotLinkParser(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available")
	}
	for _, target := range [][]string{nil, {"GOOS=js", "GOARCH=wasm"}} {
		for _, pkg := range runtimePackages {
			cmd := exec.Command(goBin, "list", "-deps", pkg)
			if target != nil {
				cmd.Env = append(cmd.Environ(), target...)
			}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list -deps %s %v: %v", pkg, target, err)
			}
			for _, dep := range strings.Fields(string(out)) {
				if forbiddenRuntimeDep(dep) {
					t.Errorf("%s %v imports %s", pkg, target, dep)
				}
			}
		}
	}
}
