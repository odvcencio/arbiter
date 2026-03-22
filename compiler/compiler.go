package compiler

import (
	"github.com/odvcencio/arbiter/ir"
	gotreesitter "github.com/odvcencio/gotreesitter"
)

// CompileCST lowers a parsed CST into the shared IR and compiles it to bytecode.
func CompileCST(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (*CompiledRuleset, error) {
	program, err := ir.Lower(root, source, lang)
	if err != nil {
		return nil, err
	}
	return CompileIR(program)
}
