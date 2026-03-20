package arbiter

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/vm"
)

// HTTPContextFunc builds an Arbiter evaluation context from an HTTP request.
type HTTPContextFunc func(*http.Request) (map[string]any, error)

// HTTPErrorHandler handles request-context build failures or evaluation failures.
type HTTPErrorHandler func(http.ResponseWriter, *http.Request, error)

// HTTPDecision captures one governed HTTP request evaluation.
type HTTPDecision struct {
	Context map[string]any     `json:"context"`
	Matched []vm.MatchedRule   `json:"matched,omitempty"`
	Trace   []govern.TraceStep `json:"trace,omitempty"`
}

// HTTPMiddlewareOptions configures MiddlewareWithOptions.
type HTTPMiddlewareOptions struct {
	BuildContext HTTPContextFunc
	OnBuildError HTTPErrorHandler
	OnEvalError  HTTPErrorHandler
}

type httpDecisionKey struct{}

// Middleware evaluates a governed ruleset for each request, stores the decision
// on the request context, and then calls next.
func Middleware(compiled *CompileResult, buildContext HTTPContextFunc, next http.Handler) http.Handler {
	return MiddlewareWithOptions(compiled, next, HTTPMiddlewareOptions{
		BuildContext: buildContext,
	})
}

// MiddlewareWithOptions evaluates a governed ruleset per request and injects the
// result into the request context for downstream handlers.
func MiddlewareWithOptions(compiled *CompileResult, next http.Handler, opts HTTPMiddlewareOptions) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}

	buildContext := opts.BuildContext
	if buildContext == nil {
		buildContext = DefaultHTTPContext
	}

	onBuildError := opts.OnBuildError
	if onBuildError == nil {
		onBuildError = defaultHTTPErrorHandler(http.StatusBadRequest, "build arbiter context")
	}

	onEvalError := opts.OnEvalError
	if onEvalError == nil {
		onEvalError = defaultHTTPErrorHandler(http.StatusInternalServerError, "evaluate arbiter rules")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if compiled == nil || compiled.Ruleset == nil {
			onEvalError(w, r, fmt.Errorf("nil compiled ruleset"))
			return
		}

		ctxMap, err := buildContext(r)
		if err != nil {
			onBuildError(w, r, err)
			return
		}
		if ctxMap == nil {
			ctxMap = map[string]any{}
		}

		dc := DataFromMap(ctxMap, compiled.Ruleset)
		matched, trace, err := EvalGoverned(compiled.Ruleset, dc, compiled.Segments, ctxMap)
		if err != nil {
			onEvalError(w, r, err)
			return
		}

		decision := HTTPDecision{
			Context: cloneAnyMap(ctxMap),
			Matched: cloneMatchedRules(matched),
			Trace:   cloneTraceSteps(trace),
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpDecisionKey{}, decision)))
	})
}

// DecisionFromRequest returns the middleware decision associated with an HTTP request.
func DecisionFromRequest(r *http.Request) (HTTPDecision, bool) {
	if r == nil {
		return HTTPDecision{}, false
	}
	return DecisionFromContext(r.Context())
}

// DecisionFromContext returns the middleware decision associated with a context.
func DecisionFromContext(ctx context.Context) (HTTPDecision, bool) {
	if ctx == nil {
		return HTTPDecision{}, false
	}
	decision, ok := ctx.Value(httpDecisionKey{}).(HTTPDecision)
	return decision, ok
}

// DefaultHTTPContext exposes normalized request metadata for HTTP middleware use.
func DefaultHTTPContext(r *http.Request) (map[string]any, error) {
	if r == nil {
		return map[string]any{}, nil
	}
	path := ""
	var query map[string]any
	if r.URL != nil {
		path = r.URL.Path
		query = normalizeHTTPValues(r.URL.Query())
	}
	return map[string]any{
		"request": map[string]any{
			"method":      r.Method,
			"host":        r.Host,
			"path":        path,
			"remote_addr": r.RemoteAddr,
			"headers":     normalizeHTTPValues(r.Header),
			"query":       query,
		},
	}, nil
}

func defaultHTTPErrorHandler(status int, prefix string) HTTPErrorHandler {
	return func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, prefix+": "+err.Error(), status)
	}
}

func normalizeHTTPValues(values map[string][]string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, items := range values {
		normalized := normalizeHTTPKey(key)
		switch len(items) {
		case 0:
			out[normalized] = ""
		case 1:
			out[normalized] = coerceHTTPScalar(items[0])
		default:
			list := make([]any, 0, len(items))
			for _, item := range items {
				list = append(list, coerceHTTPScalar(item))
			}
			out[normalized] = list
		}
	}
	return out
}

func normalizeHTTPKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return "value"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	normalized := strings.Trim(b.String(), "_")
	if normalized == "" {
		return "value"
	}
	return normalized
}

func coerceHTTPScalar(raw string) any {
	if b, err := strconv.ParseBool(raw); err == nil {
		return b
	}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		return n
	}
	return raw
}

func cloneMatchedRules(matched []vm.MatchedRule) []vm.MatchedRule {
	if len(matched) == 0 {
		return nil
	}
	out := make([]vm.MatchedRule, len(matched))
	for i, match := range matched {
		out[i] = vm.MatchedRule{
			Name:     match.Name,
			Priority: match.Priority,
			Action:   match.Action,
			Params:   cloneAnyMap(match.Params),
			Fallback: match.Fallback,
		}
	}
	return out
}

func cloneTraceSteps(trace *govern.Trace) []govern.TraceStep {
	if trace == nil || len(trace.Steps) == 0 {
		return nil
	}
	out := make([]govern.TraceStep, len(trace.Steps))
	copy(out, trace.Steps)
	return out
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = cloneAnyValue(value)
	}
	return dst
}

func cloneAnyValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		return cloneAnyMap(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneAnyValue(item)
		}
		return out
	case []string:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = item
		}
		return out
	case []float64:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = item
		}
		return out
	case []int:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = item
		}
		return out
	case []bool:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = item
		}
		return out
	default:
		return value
	}
}
