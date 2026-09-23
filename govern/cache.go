package govern

import (
	"fmt"
	"time"
)

type flagResult struct {
	variant string
	def     string
}

// RequestCache memoizes governance checks within a single evaluation pass.
type RequestCache struct {
	segments    *SegmentSet
	ctx         map[string]any
	nestedCtx   map[string]any
	segResults  map[string]bool
	segErrs     map[string]error
	ruleResults map[string]bool
	flagResults map[string]flagResult
	evalStack   map[string]bool
	evalTime    time.Time
	hasEvalTime bool
}

// NewRequestCache creates a per-request cache.
func NewRequestCache(segments *SegmentSet, ctx map[string]any) *RequestCache {
	if ctx == nil {
		ctx = map[string]any{}
	}
	return &RequestCache{
		segments:    segments,
		ctx:         ctx,
		nestedCtx:   NestDottedKeys(ctx),
		segResults:  make(map[string]bool),
		segErrs:     make(map[string]error),
		ruleResults: make(map[string]bool),
		flagResults: make(map[string]flagResult),
		evalStack:   make(map[string]bool),
	}
}

// Context returns the original flat request context.
func (rc *RequestCache) Context() map[string]any {
	if rc == nil {
		return nil
	}
	return rc.ctx
}

// NestedContext returns the nested context used for segment evaluation.
func (rc *RequestCache) NestedContext() map[string]any {
	if rc == nil {
		return nil
	}
	return rc.nestedCtx
}

// EvalTime returns the effective evaluation time for this request.
func (rc *RequestCache) EvalTime() time.Time {
	if rc == nil {
		return time.Now().UTC()
	}
	if !rc.hasEvalTime {
		rc.evalTime = resolveEvalTime(rc.ctx)
		rc.hasEvalTime = true
	}
	return rc.evalTime
}

// EvalSegment evaluates a segment with memoization. A non-nil error means the
// segment's condition failed at runtime (not that it evaluated to false);
// callers must surface it the same way they surface a rule condition error,
// consistent with the rest of governed evaluation.
func (rc *RequestCache) EvalSegment(name string) (bool, string, error) {
	if rc == nil {
		return false, fmt.Sprintf("%s -> false", name), nil
	}
	if result, ok := rc.segResults[name]; ok {
		if err := rc.segErrs[name]; err != nil {
			return false, fmt.Sprintf("segment %q error: %v (cached)", name, err), err
		}
		return result, fmt.Sprintf("%s -> %v (cached)", name, result), nil
	}
	seg, ok := rc.segments.Get(name)
	if !ok {
		rc.segResults[name] = false
		return false, fmt.Sprintf("segment %q not found", name), nil
	}
	matched, err := seg.Eval(rc.nestedCtx)
	if err != nil {
		rc.segResults[name] = false
		rc.segErrs[name] = err
		return false, fmt.Sprintf("segment %q error: %v", name, err), err
	}
	rc.segResults[name] = matched
	return matched, fmt.Sprintf("%s -> %v", seg.Source, matched), nil
}

// RecordRuleResult records whether a rule matched.
func (rc *RequestCache) RecordRuleResult(name string, matched bool) {
	if rc == nil {
		return
	}
	rc.ruleResults[name] = matched
}

// RecordFlagResult records a flag's resolved variant.
func (rc *RequestCache) RecordFlagResult(key string, variant string, defaultVariant string) {
	if rc == nil {
		return
	}
	rc.flagResults[key] = flagResult{variant: variant, def: defaultVariant}
}

// FlagVariant returns a cached flag result if present.
func (rc *RequestCache) FlagVariant(key string) (string, bool) {
	if rc == nil {
		return "", false
	}
	res, ok := rc.flagResults[key]
	return res.variant, ok
}

// PrerequisiteMet checks whether a named prerequisite passed.
func (rc *RequestCache) PrerequisiteMet(name string) bool {
	if rc == nil {
		return false
	}
	if matched, ok := rc.ruleResults[name]; ok {
		return matched
	}
	if res, ok := rc.flagResults[name]; ok {
		return res.variant != res.def
	}
	return false
}

// CheckPrerequisites verifies all prerequisites are met and records arbitrace steps.
func (rc *RequestCache) CheckPrerequisites(prereqs []string, arbitrace *Arbitrace) bool {
	return rc.CheckPrerequisitesFor("", "", prereqs, arbitrace)
}

// CheckPrerequisitesFor verifies all prerequisites are met and records arbitrace
// steps with a structured scope/subject.
func (rc *RequestCache) CheckPrerequisitesFor(scope, subject string, prereqs []string, arbitrace *Arbitrace) bool {
	for _, prereq := range prereqs {
		ok := rc.PrerequisiteMet(prereq)
		arbitrace.AppendScoped(ArbitracePhaseGovernance, scope, subject, ArbitraceKindRequires, prereq, "", ok, fmt.Sprintf("%s -> %v", prereq, ok))
		if !ok {
			return false
		}
	}
	return true
}

// CheckExclusions verifies no excluded rules matched. Returns false if any
// exclusion matched. Also returns false if an excluded rule hasn't been
// evaluated yet — we can't safely proceed without knowing.
func (rc *RequestCache) CheckExclusions(excludes []string, arbitrace *Arbitrace) bool {
	return rc.CheckExclusionsFor("", "", excludes, arbitrace)
}

// CheckExclusionsFor verifies no excluded rules matched and records arbitrace steps
// with a structured scope/subject.
func (rc *RequestCache) CheckExclusionsFor(scope, subject string, excludes []string, arbitrace *Arbitrace) bool {
	if rc == nil {
		return true
	}
	for _, excl := range excludes {
		if _, evaluated := rc.ruleResults[excl]; !evaluated {
			// Rule hasn't been evaluated yet — defer this rule until later
			arbitrace.AppendDeferredScoped(ArbitracePhaseGovernance, scope, subject, ArbitraceKindExcludes, excl, "", fmt.Sprintf("%s not yet evaluated", excl))
			return false
		}
		matched := rc.ruleResults[excl]
		ok := !matched
		arbitrace.AppendScoped(ArbitracePhaseGovernance, scope, subject, ArbitraceKindExcludes, excl, "", ok, fmt.Sprintf("%s matched=%v", excl, matched))
		if !ok {
			return false
		}
	}
	return true
}

// HasCycle reports whether the given key is already being evaluated.
func (rc *RequestCache) HasCycle(name string) bool {
	if rc == nil {
		return false
	}
	return rc.evalStack[name]
}

// Enter marks a key as in-progress.
func (rc *RequestCache) Enter(name string) {
	if rc == nil {
		return
	}
	rc.evalStack[name] = true
}

// Leave clears the in-progress marker for a key.
func (rc *RequestCache) Leave(name string) {
	if rc == nil {
		return
	}
	delete(rc.evalStack, name)
}
