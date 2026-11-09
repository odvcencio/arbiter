package build_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/arbiter/gameai"
	"m31labs.dev/arbiter/gameai/build"
)

const src = `
tag "move"
tag "talk"
rule Walk tag "move" { when { speed < 3 } then Move { kind: "walk" } }
rule Run tag "move" { when { speed >= 3 } then Move { kind: "run" } }
rule Hi tag "talk" { when { true } then Say { line: "hi" } }
`

func TestCompileIsDeterministicAndLoadable(t *testing.T) {
	a, err := build.Compile([]byte(src), build.Options{KeepNames: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := build.Compile([]byte(src), build.Options{KeepNames: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bundle, b.Bundle) || a.Digest != b.Digest {
		t.Fatal("two builds of the same source differ")
	}
	p, err := gameai.Load(a.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	if p.DigestHex() != a.Digest || a.Rules != 3 || !reflect.DeepEqual(a.Tags, []string{"move", "talk"}) {
		t.Fatalf("digest %s vs %s, rules %d, tags %v", p.DigestHex(), a.Digest, a.Rules, a.Tags)
	}
	if !reflect.DeepEqual(p.Rules(), []string{"Walk", "Run", "Hi"}) {
		t.Fatalf("rules %v", p.Rules())
	}
	hashed, err := build.Compile([]byte(src), build.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if hashed.Program.Rules()[0] == "Walk" || hashed.Digest == a.Digest {
		t.Fatal("rule names were not hashed")
	}
}

func TestCompileRejectsWhatBundlesCannotCarry(t *testing.T) {
	for name, bad := range map[string]string{
		"strategy": `outcome O { k: string }
strategy S returns O { when { x > 1 } then A { k: "a" } else B { k: "b" } }`,
		"flag":   `flag f type boolean default "off" { variant "on" { x: true } when { y > 1 } then "on" }`,
		"expert": `expert rule E { when { true } then emit Out { k: "x" } }`,
		"window": `rule R { active_until 2030-01-01T00:00:00Z when { true } then A {} }`,
	} {
		if _, err := build.Compile([]byte(bad), build.Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCompileFileResolvesIncludes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shared.arb"), []byte("const LIMIT = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := "include \"shared.arb\"\nrule Run { when { speed >= LIMIT } then Move { kind: \"run\" } }\n"
	path := filepath.Join(dir, "ai.arb")
	if err := os.WriteFile(path, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := build.CompileFile(path, build.Options{KeepNames: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rules != 1 || !strings.HasPrefix(res.Program.Rules()[0], "Run") {
		t.Fatalf("result %+v", res)
	}
}
