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

// EvalContext bundles a DataContext with its StringPool so the VM can resolve
// both compile-time and runtime-interned strings.
type EvalContext struct {
	DC   vm.DataContext
	Pool *vm.StringPool
}

// Eval evaluates a compiled ruleset against a data context.
func Eval(rs *compiler.CompiledRuleset, dc vm.DataContext) ([]vm.MatchedRule, error) {
	// If dc was created via DataFromMap/DataFromJSON, it shares a pool.
	// Try to extract it; otherwise create a new one.
	if ec, ok := dc.(*evalContextWrapper); ok {
		return vm.EvalWithPool(rs, ec.inner, ec.pool)
	}
	return vm.Eval(rs, dc)
}

// EvalDebug evaluates with full debug trace.
func EvalDebug(rs *compiler.CompiledRuleset, dc vm.DataContext) vm.DebugResult {
	return vm.EvalDebug(rs, dc)
}

// evalContextWrapper wraps a DataContext with its StringPool.
type evalContextWrapper struct {
	inner vm.DataContext
	pool  *vm.StringPool
}

func (w *evalContextWrapper) Get(key string) vm.Value {
	return w.inner.Get(key)
}

// DataFromMap creates a DataContext from a Go map.
// The returned DataContext shares a StringPool with the evaluator.
func DataFromMap(m map[string]any, rs *compiler.CompiledRuleset) vm.DataContext {
	pool := vm.NewStringPool(rs.Constants.Strings())
	dc := vm.DataFromMap(m, pool)
	return &evalContextWrapper{inner: dc, pool: pool}
}

// DataFromJSON creates a DataContext from JSON.
func DataFromJSON(jsonStr string, rs *compiler.CompiledRuleset) (vm.DataContext, error) {
	pool := vm.NewStringPool(rs.Constants.Strings())
	dc, err := vm.DataFromJSON(jsonStr, pool)
	if err != nil {
		return nil, err
	}
	return &evalContextWrapper{inner: dc, pool: pool}, nil
}
