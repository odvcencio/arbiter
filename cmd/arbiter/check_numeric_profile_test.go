package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkIntegerProfile(t *testing.T, src string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.arb")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return runCheck([]string{path, "--numeric-profile", "integer"})
}

func TestCheckNumericProfileIntegerAcceptsIntegerPolicy(t *testing.T) {
	src := `rule R { when { floor(a / 2) + b % 3 > 4 } then Out { v: round(a / 3) * 2 } }`
	if err := checkIntegerProfile(t, src); err != nil {
		t.Fatalf("conforming policy rejected: %v", err)
	}
}

func TestCheckNumericProfileIntegerRejects(t *testing.T) {
	cases := map[string]string{
		"non-integer literal": `rule R { when { a > 1.5 } then Out {} }`,
		"decimal":             `rule R { when { amount > 10.50 USD } then Out {} }`,
		"unrounded division":  `rule R { when { a / b > 2 } then Out {} }`,
		"expert rule":         `expert rule E { when { a / b > 2 } then emit Out { v: 1 } }`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			err := checkIntegerProfile(t, src)
			if err == nil || !strings.Contains(err.Error(), "integer numeric profile") {
				t.Fatalf("expected an integer-profile violation, got %v", err)
			}
		})
	}
}

func TestCheckNumericProfileIsOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.arb")
	if err := os.WriteFile(path, []byte(`rule R { when { a / b > 1.5 } then Out {} }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCheck([]string{path}); err != nil {
		t.Fatalf("plain check must accept float rules: %v", err)
	}
	if err := runCheck([]string{path, "--numeric-profile", "bogus"}); err == nil {
		t.Fatal("unknown profile must be rejected")
	}
}
