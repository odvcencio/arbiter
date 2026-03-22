package arbiter

import (
	"fmt"

	"github.com/odvcencio/arbiter/strategy"
)

// CompileStrategies compiles all strategy declarations from raw .arb source.
func CompileStrategies(source []byte) (*strategy.Strategies, error) {
	full, err := CompileFull(source)
	if err != nil {
		return nil, err
	}
	return full.Strategies, nil
}

// CompileStrategiesParsed compiles all strategy declarations from parsed source.
func CompileStrategiesParsed(parsed *ParsedSource) (*strategy.Strategies, error) {
	full, err := CompileFullParsed(parsed)
	if err != nil {
		return nil, err
	}
	return full.Strategies, nil
}

// CompileStrategiesFile resolves includes and compiles all strategy declarations.
func CompileStrategiesFile(path string) (*strategy.Strategies, error) {
	full, err := CompileFullFile(path)
	if err != nil {
		return nil, err
	}
	return full.Strategies, nil
}

// EvalStrategy evaluates one compiled strategy against the given request context.
func EvalStrategy(compiled *CompileResult, name string, ctx map[string]any) (strategy.Result, error) {
	if compiled == nil {
		return strategy.Result{}, fmt.Errorf("nil compiled program")
	}
	if compiled.Strategies == nil {
		return strategy.Result{}, fmt.Errorf("nil compiled strategies")
	}
	return compiled.Strategies.Evaluate(name, ctx)
}
