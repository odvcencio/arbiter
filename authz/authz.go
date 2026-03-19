package authz

import (
	"fmt"
	"strings"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/vm"
)

// Request is the conventional actor/action/resource authz envelope.
type Request struct {
	Actor    map[string]any
	Action   string
	Resource map[string]any
	Context  map[string]any
}

// Decision is the result of one authorization evaluation.
type Decision struct {
	Allowed bool
	Matched []vm.MatchedRule
	Trace   []govern.TraceStep
}

// BuildContext constructs the canonical authz evaluation context.
func BuildContext(req Request) map[string]any {
	ctx := make(map[string]any, len(req.Context)+3)
	for key, value := range req.Context {
		ctx[key] = value
	}
	ctx["actor"] = req.Actor
	ctx["action"] = req.Action
	ctx["resource"] = req.Resource
	return ctx
}

// Evaluate runs a compiled governed ruleset against an authz request.
func Evaluate(compiled *arbiter.CompileResult, req Request) (Decision, error) {
	if compiled == nil || compiled.Ruleset == nil {
		return Decision{}, fmt.Errorf("nil compiled ruleset")
	}

	ctx := BuildContext(req)
	dc := arbiter.DataFromMap(ctx, compiled.Ruleset)
	matched, trace, err := arbiter.EvalGoverned(compiled.Ruleset, dc, compiled.Segments, ctx)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Allowed: hasAllowMatch(matched),
		Matched: matched,
		Trace:   trace.Steps,
	}, nil
}

// EvaluateSource compiles raw source and evaluates it as an authz request.
func EvaluateSource(source []byte, req Request) (Decision, error) {
	compiled, err := arbiter.CompileFull(source)
	if err != nil {
		return Decision{}, err
	}
	return Evaluate(compiled, req)
}

// EvaluateFile compiles a file-backed .arb program and evaluates it as an authz request.
func EvaluateFile(path string, req Request) (Decision, error) {
	compiled, err := arbiter.CompileFullFile(path)
	if err != nil {
		return Decision{}, err
	}
	return Evaluate(compiled, req)
}

func hasAllowMatch(matched []vm.MatchedRule) bool {
	for _, match := range matched {
		if strings.EqualFold(match.Action, "Allow") {
			return true
		}
	}
	return false
}
