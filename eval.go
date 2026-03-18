package arbiter

import (
	"fmt"

	gotreesitter "github.com/odvcencio/gotreesitter"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/vm"
)

// Compile compiles .arb source into a CompiledRuleset.
func Compile(source []byte) (*compiler.CompiledRuleset, error) {
	lang, err := GetLanguage()
	if err != nil {
		return nil, fmt.Errorf("get language: %w", err)
	}

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	root := tree.RootNode()
	if root.HasError() {
		return nil, fmt.Errorf("parse errors in arbiter source")
	}

	return compiler.CompileCST(root, source, lang)
}

// CompileJSON compiles a single Arishem JSON rule.
func CompileJSON(condJSON, actJSON string) (*compiler.CompiledRuleset, error) {
	return compiler.CompileJSONRule("rule0", 0, condJSON, actJSON)
}

// CompileJSONRules compiles a batch of Arishem JSON rules.
func CompileJSONRules(rules []compiler.JSONRuleInput) (*compiler.CompiledRuleset, error) {
	return compiler.CompileJSONBatch(rules)
}

// Eval evaluates a compiled ruleset against a data context.
func Eval(rs *compiler.CompiledRuleset, dc vm.DataContext) ([]vm.MatchedRule, error) {
	return vm.Eval(rs, dc)
}

// EvalDebug evaluates with full debug trace.
func EvalDebug(rs *compiler.CompiledRuleset, dc vm.DataContext) vm.DebugResult {
	return vm.EvalDebug(rs, dc)
}

// DataFromMap creates a DataContext from a Go map. Pass the ruleset's string pool.
func DataFromMap(m map[string]any, rs *compiler.CompiledRuleset) vm.DataContext {
	pool := vm.NewStringPool(rs.Constants.Strings())
	return vm.DataFromMap(m, pool)
}

// DataFromJSON creates a DataContext from JSON. Pass the ruleset's string pool.
func DataFromJSON(jsonStr string, rs *compiler.CompiledRuleset) (vm.DataContext, error) {
	pool := vm.NewStringPool(rs.Constants.Strings())
	return vm.DataFromJSON(jsonStr, pool)
}
