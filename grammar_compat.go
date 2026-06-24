package arbiter

import (
	gotreesitter "github.com/odvcencio/gotreesitter"

	"m31labs.dev/arbiter/grammar"
	"m31labs.dev/arbiter/grammar/dsl"
)

//go:generate go run ./cmd/arbiter-grammar

// The Arbiter tree-sitter grammar now lives in two subpackages so highlighters
// can consume it without the arbiter runtime — and without the gotreesitter
// grammar registry:
//   - grammar      — consumable: Language(), Highlights, Highlighter(); deps =
//     gotreesitter core only (no grammargen/grammars).
//   - grammar/dsl  — the grammar DSL, used only to regenerate grammar.bin.
//
// These shims preserve the historical root-package API.

// Grammar and Rule are the grammar DSL types, re-exported from the dsl
// subpackage.
type (
	Grammar = dsl.Grammar
	Rule    = dsl.Rule
)

// ArbiterGrammar returns the Arbiter rule-engine grammar DSL definition.
func ArbiterGrammar() *Grammar { return dsl.ArbiterGrammar() }

// GetLanguage returns the compiled Arbiter tree-sitter language. Safe for
// concurrent use (internally cached).
func GetLanguage() (*gotreesitter.Language, error) { return grammar.Language() }
