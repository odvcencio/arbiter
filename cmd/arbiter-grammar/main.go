// Command arbiter-grammar regenerates the pre-built parser artifacts from
// ArbiterGrammar(): the embedded parse-table blob (grammar.bin, loaded at
// runtime ~75x faster than rebuilding the table) and the exported tree-sitter
// grammar (grammar.json, for editors/external tooling).
//
// Run it after any change to the grammar:
//
//	go generate ./...
//
// The TestGrammarBinIsCurrent drift test fails if grammar.bin is out of date.
package main

import (
	"fmt"
	"os"

	"github.com/odvcencio/gotreesitter/grammargen"
	"m31labs.dev/arbiter/grammar/dsl"
)

func main() {
	g := dsl.ArbiterGrammar()

	_, blob, err := grammargen.GenerateLanguageAndBlob(g)
	if err != nil {
		fmt.Fprintln(os.Stderr, "arbiter-grammar: generate parse table:", err)
		os.Exit(1)
	}
	if err := os.WriteFile("grammar/grammar.bin", blob, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "arbiter-grammar: write grammar.bin:", err)
		os.Exit(1)
	}

	jsonBytes, err := grammargen.ExportGrammarJSON(g)
	if err != nil {
		fmt.Fprintln(os.Stderr, "arbiter-grammar: export grammar json:", err)
		os.Exit(1)
	}
	if err := os.WriteFile("grammar/grammar.json", jsonBytes, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "arbiter-grammar: write grammar.json:", err)
		os.Exit(1)
	}

	fmt.Printf("regenerated grammar.bin (%d bytes) and grammar.json (%d bytes)\n", len(blob), len(jsonBytes))
}
