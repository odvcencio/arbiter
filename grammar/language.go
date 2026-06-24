// Package grammar is the consumable Arbiter tree-sitter grammar: it loads the
// pre-generated parse table and exposes a highlighter. It deliberately depends
// only on the gotreesitter core (LoadLanguage / NewHighlighter) — NOT on
// gotreesitter/grammargen or gotreesitter/grammars — so highlighters can embed
// the Arbiter grammar without dragging in the ~200-grammar registry. The grammar
// DSL (used only to regenerate grammar.bin) lives in the grammar/dsl subpackage.
package grammar

import (
	_ "embed"
	"sync"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// grammarBlob is the pre-generated parse table, regenerated from
// dsl.ArbiterGrammar() by `go generate ./...` (see cmd/arbiter-grammar). Loading
// it is ~75x faster than rebuilding the table from the grammar DSL.
// TestGrammarBinIsCurrent guards it against drift.
//
//go:embed grammar.bin
var grammarBlob []byte

// Highlights is the tree-sitter highlight query for Arbiter source, ready to
// pass to gotreesitter.NewHighlighter. Exported so external highlighters
// (editors, the m31labs.dev product pages, gts) can consume the Arbiter grammar
// without importing the arbiter runtime module.
//
//go:embed highlights.scm
var Highlights string

var (
	langOnce  sync.Once
	langCache *gotreesitter.Language
	langErr   error

	hlOnce  sync.Once
	hlCache *gotreesitter.Highlighter
	hlErr   error
)

// Language returns the compiled Arbiter tree-sitter language, loaded from the
// embedded pre-generated parse table. Safe for concurrent use (cached).
func Language() (*gotreesitter.Language, error) {
	langOnce.Do(func() {
		langCache, langErr = gotreesitter.LoadLanguage(grammarBlob)
	})
	return langCache, langErr
}

// Highlighter returns a cached gotreesitter Highlighter for Arbiter source,
// combining Language() with the Highlights query. This is the one-call entry
// point for consumers that just want to syntax-highlight .arb code.
func Highlighter() (*gotreesitter.Highlighter, error) {
	hlOnce.Do(func() {
		lang, err := Language()
		if err != nil {
			hlErr = err
			return
		}
		hlCache, hlErr = gotreesitter.NewHighlighter(lang, Highlights)
	})
	return hlCache, hlErr
}
