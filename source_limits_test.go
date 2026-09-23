package arbiter

import (
	"strings"
	"testing"
)

func TestSourceNestingDepth(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"empty", "", 0},
		{"flat", `rule R { when { x > 1 } then A {} }`, 2},
		{
			"deep_parens",
			"rule R { when { true } then A { v: " + strings.Repeat("(", 10) + "1" + strings.Repeat(")", 10) + " } }",
			// the enclosing rule/when/action braces (2) plus 10 nested parens
			12,
		},
		{
			"brackets_in_string_literal_do_not_count",
			`rule R { when { name == "((((((((((" } then A {} }`,
			2,
		},
		{
			"brackets_in_hash_comment_do_not_count",
			"# ((((((((((\nrule R { when { true } then A {} }",
			2,
		},
		{
			"brackets_in_slash_comment_do_not_count",
			"// ((((((((((\nrule R { when { true } then A {} }",
			2,
		},
		{
			"brackets_in_block_comment_do_not_count",
			"/* (((((((((( */\nrule R { when { true } then A {} }",
			2,
		},
		{
			"unterminated_block_comment_does_not_hang",
			"/* (((((((((( ",
			0,
		},
		{
			"unterminated_string_does_not_hang",
			`"((((((((((`,
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceNestingDepth([]byte(tc.src)); got != tc.want {
				t.Errorf("sourceNestingDepth(%q) = %d, want %d", tc.src, got, tc.want)
			}
		})
	}
}

// TestCompileRejectsOversizedSource guards MaxSourceBytes: it must reject
// source before ever reaching the parser, with a clear diagnostic.
func TestCompileRejectsOversizedSource(t *testing.T) {
	orig := MaxSourceBytes
	MaxSourceBytes = 64
	defer func() { MaxSourceBytes = orig }()

	src := []byte(`rule R { when { user.score > 0 and user.plan == "enterprise" } then A {} }`)
	if len(src) <= MaxSourceBytes {
		t.Fatalf("test fixture (%d bytes) must exceed the test limit (%d bytes)", len(src), MaxSourceBytes)
	}

	_, err := Compile(src)
	if err == nil {
		t.Fatal("Compile: expected an error for oversized source, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), "byte") {
		t.Fatalf("Compile: expected a source-size diagnostic, got: %v", err)
	}
}

// TestCompileRejectsExcessiveSourceNesting guards MaxSourceNestingDepth: it
// must reject deeply nested source before ever reaching the parser (the
// slow path for gotreesitter's GLR conflict resolution on nested parens),
// with a clear diagnostic.
func TestCompileRejectsExcessiveSourceNesting(t *testing.T) {
	orig := MaxSourceNestingDepth
	MaxSourceNestingDepth = 50
	defer func() { MaxSourceNestingDepth = orig }()

	depth := 60
	src := []byte("rule R { when { true } then A { v: " +
		strings.Repeat("(", depth) + "1" + strings.Repeat(")", depth) +
		" } }")

	_, err := Compile(src)
	if err == nil {
		t.Fatal("Compile: expected an error for excessive source nesting, got nil")
	}
	if !strings.Contains(err.Error(), "nests") || !strings.Contains(err.Error(), "level") {
		t.Fatalf("Compile: expected a source-nesting diagnostic, got: %v", err)
	}
}

// TestCompileAcceptsOrdinarySourceUnderLimits guards against the new limits
// rejecting real, moderately sized and nested .arb files at their defaults.
func TestCompileAcceptsOrdinarySourceUnderLimits(t *testing.T) {
	for _, f := range []string{"testdata/pricing.arb", "testdata/fraud.arb", "testdata/moderation.arb"} {
		t.Run(f, func(t *testing.T) {
			if _, err := CompileFile(f); err != nil {
				t.Fatalf("CompileFile(%s): unexpected error under default limits: %v", f, err)
			}
		})
	}
}
