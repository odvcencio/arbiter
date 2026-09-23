package flags

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/overrides"
)

// Flags is a compiled flag ruleset with first-class flag concepts and rich explainability.
type Flags struct {
	mu          sync.RWMutex
	defs        map[string]*FlagDef
	segments    *govern.SegmentSet
	source      []byte
	Environment string // set by LoadEnv; empty for non-environment loads
}

// Load parses .arb source, extracts flags + segments, and compiles segments.
func Load(source []byte) (*Flags, error) {
	parsed, err := arbiter.ParseSource(source)
	if err != nil {
		return nil, err
	}
	full, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, err
	}
	return LoadParsed(parsed, full)
}

// LoadParsed parses flags from a previously parsed source and compiled segment set.
func LoadParsed(parsed *arbiter.ParsedSource, full *arbiter.CompileResult) (*Flags, error) {
	f := &Flags{}
	if err := f.parseParsed(parsed, full); err != nil {
		return nil, err
	}
	return f, nil
}

// LoadFile loads and compiles flags from a file path.
func LoadFile(path string) (*Flags, error) {
	unit, parsed, err := arbiter.LoadFileParsed(path)
	if err != nil {
		return nil, err
	}
	// CompileFullFile (not CompileFullParsed) so imports resolve: a flag bundle
	// may import flags/segments from other modules, which must be merged before
	// the flag definitions are lowered.
	full, err := arbiter.CompileFullFile(path)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	f, err := LoadParsed(parsed, full)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	return f, nil
}

// LoadEnv loads flags for a specific environment.
// Looks for <dir>/<env>.arb and loads it.
func LoadEnv(dir, env string) (*Flags, error) {
	path := dir + "/" + env + ".arb"
	f, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	f.Environment = env
	return f, nil
}

// LoadFileWithEnv loads a base .arb file and merges an environment overlay:
//
//	flags.arb              → base definitions (always loaded)
//	flags.production.arb   → environment overrides (merged on top)
//
// The environment file can redefine any flag from the base. Flags not
// redefined in the environment file keep their base definitions.
func LoadFileWithEnv(path, env string) (*Flags, error) {
	base, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	base.Environment = env

	if env == "" {
		return base, nil
	}

	// Derive environment file: flags.arb → flags.production.arb
	envPath := envFilePath(path, env)
	envSource, readErr := os.ReadFile(envPath)
	if readErr != nil {
		// No environment file — base only
		return base, nil
	}

	envFlags, err := Load(envSource)
	if err != nil {
		return nil, fmt.Errorf("environment %s: %w", env, err)
	}

	// Merge: environment flags override base flags by key
	base.mu.Lock()
	for key, def := range envFlags.defs {
		base.defs[key] = def
	}
	if envFlags.segments != nil {
		for _, seg := range envFlags.segments.All() {
			base.segments.Add(seg)
		}
	}
	base.mu.Unlock()

	return base, nil
}

// envFilePath converts "flags.arb" + "production" → "flags.production.arb"
func envFilePath(base, env string) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return stem + "." + env + ext
}

// Reload atomically re-parses and swaps the flag definitions from new source.
func (f *Flags) Reload(source []byte) error {
	parsed, err := arbiter.ParseSource(source)
	if err != nil {
		return err
	}
	full, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return err
	}
	newF := &Flags{}
	if err := newF.parseParsed(parsed, full); err != nil {
		return err
	}
	f.mu.Lock()
	f.defs = newF.defs
	f.segments = newF.segments
	f.source = newF.source
	f.mu.Unlock()
	return nil
}

// ReloadFile atomically reloads from a file path.
func (f *Flags) ReloadFile(path string) error {
	unit, parsed, err := arbiter.LoadFileParsed(path)
	if err != nil {
		return err
	}
	full, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return arbiter.WrapFileError(unit, err)
	}
	newF := &Flags{}
	if err := newF.parseParsed(parsed, full); err != nil {
		return arbiter.WrapFileError(unit, err)
	}
	f.mu.Lock()
	f.defs = newF.defs
	f.segments = newF.segments
	f.source = newF.source
	f.mu.Unlock()
	return nil
}

// Enabled returns true if the flag is on for boolean flags,
// or non-default for multivariate flags.
func (f *Flags) Enabled(flag string, ctx map[string]any) bool {
	f.mu.RLock()
	def, ok := f.defs[flag]
	f.mu.RUnlock()
	if !ok {
		return false
	}
	rc := govern.NewRequestCache(f.segments, ctx)
	v := f.evalVariantName(def, rc, nil, "", nil)
	if def.Type == FlagBoolean {
		return v == "true"
	}
	return v != def.Default
}

// Variant returns the served variant with its payload for a flag.
func (f *Flags) Variant(flag string, ctx map[string]any) ServedVariant {
	f.mu.RLock()
	def, ok := f.defs[flag]
	f.mu.RUnlock()
	if !ok {
		return ServedVariant{}
	}
	rc := govern.NewRequestCache(f.segments, ctx)
	name := f.evalVariantName(def, rc, nil, "", nil)
	return f.resolveVariant(def, name)
}

// VariantName returns just the variant name string (for backward compat / simple use).
func (f *Flags) VariantName(flag string, ctx map[string]any) string {
	f.mu.RLock()
	def, ok := f.defs[flag]
	f.mu.RUnlock()
	if !ok {
		return ""
	}
	rc := govern.NewRequestCache(f.segments, ctx)
	return f.evalVariantName(def, rc, nil, "", nil)
}

// AllFlags returns all flag variants for the given context.
func (f *Flags) AllFlags(ctx map[string]any) map[string]ServedVariant {
	f.mu.RLock()
	defs := f.defs
	f.mu.RUnlock()

	rc := govern.NewRequestCache(f.segments, ctx)
	result := make(map[string]ServedVariant, len(defs))
	for key, def := range defs {
		name := f.evalVariantName(def, rc, nil, "", nil)
		result[key] = f.resolveVariantRedacted(def, name)
	}
	return result
}

// Count returns the number of loaded flags.
func (f *Flags) Count() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.defs)
}

// Has reports whether a flag key exists.
func (f *Flags) Has(flag string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.defs[flag]
	return ok
}

// RuleCount returns the number of targeting rules for a flag.
func (f *Flags) RuleCount(flag string) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	def, ok := f.defs[flag]
	if !ok {
		return 0
	}
	return len(def.Rules)
}

// resolveVariant builds a ServedVariant from a variant name,
// merging defaults with variant-specific values.
// Secret references are preserved as SecretValue — resolution happens at the core eval layer.
// For display (Explain, AllFlags HTTP), use resolveVariantRedacted.
func (f *Flags) resolveVariant(def *FlagDef, name string) ServedVariant {
	return buildServedVariant(def, name, false)
}

func (f *Flags) resolveVariantRedacted(def *FlagDef, name string) ServedVariant {
	return buildServedVariant(def, name, true)
}

func buildServedVariant(def *FlagDef, name string, redact bool) ServedVariant {
	sv := ServedVariant{Name: name}

	if len(def.Variants) == 0 && len(def.DefaultValues) == 0 {
		return sv
	}

	// Start with defaults
	if len(def.DefaultValues) > 0 {
		sv.Values = make(map[string]any, len(def.DefaultValues))
		for k, v := range def.DefaultValues {
			sv.Values[k] = displayValue(v, redact)
		}
	}

	// Overlay variant-specific values
	if vd, ok := def.Variants[name]; ok {
		if sv.Values == nil {
			sv.Values = make(map[string]any, len(vd.Values))
		}
		for k, v := range vd.Values {
			sv.Values[k] = displayValue(v, redact)
		}
	}

	return sv
}

// displayValue handles SecretValue for display purposes.
func displayValue(v any, redact bool) any {
	sv, ok := v.(SecretValue)
	if !ok {
		return v
	}
	if redact {
		return "[REDACTED]"
	}
	// Non-redacted: show the reference (not the resolved value — that's the core's job)
	return fmt.Sprintf("secret(%q)", sv.Ref)
}

// Explain evaluates a flag and returns a rich arbitrace of the evaluation.
func (f *Flags) Explain(flag string, ctx map[string]any) FlagEvaluation {
	start := time.Now()

	f.mu.RLock()
	def, ok := f.defs[flag]
	f.mu.RUnlock()

	if !ok {
		return FlagEvaluation{
			Flag:      flag,
			Variant:   ServedVariant{},
			IsDefault: true,
			Reason:    "flag not found",
			Elapsed:   time.Since(start),
		}
	}

	trace := &govern.Arbitrace{}
	rc := govern.NewRequestCache(f.segments, ctx)
	name := f.evalVariantName(def, rc, trace, "", nil)

	reason := buildReason(def, name, trace.Steps)

	return FlagEvaluation{
		Flag:      flag,
		Variant:   f.resolveVariantRedacted(def, name),
		IsDefault: name == def.Default,
		Reason:    reason,
		Arbitrace: trace.Steps,
		Metadata:  def.Metadata,
		Elapsed:   time.Since(start),
	}
}

// VariantWithOverrides resolves a flag while applying runtime overrides.
func (f *Flags) VariantWithOverrides(bundleID, flag string, ctx map[string]any, view overrides.View) ServedVariant {
	f.mu.RLock()
	def, ok := f.defs[flag]
	f.mu.RUnlock()
	if !ok {
		return ServedVariant{}
	}
	rc := govern.NewRequestCache(f.segments, ctx)
	name := f.evalVariantName(def, rc, nil, bundleID, view)
	return f.resolveVariant(def, name)
}

// ExplainWithOverrides resolves a flag with explainability and runtime overrides.
func (f *Flags) ExplainWithOverrides(bundleID, flag string, ctx map[string]any, view overrides.View) FlagEvaluation {
	start := time.Now()

	f.mu.RLock()
	def, ok := f.defs[flag]
	f.mu.RUnlock()
	if !ok {
		return FlagEvaluation{
			Flag:      flag,
			Variant:   ServedVariant{},
			IsDefault: true,
			Reason:    "flag not found",
			Elapsed:   time.Since(start),
		}
	}

	trace := &govern.Arbitrace{}
	rc := govern.NewRequestCache(f.segments, ctx)
	name := f.evalVariantName(def, rc, trace, bundleID, view)

	return FlagEvaluation{
		Flag:      flag,
		Variant:   f.resolveVariantRedacted(def, name),
		IsDefault: name == def.Default,
		Reason:    buildReason(def, name, trace.Steps),
		Arbitrace: trace.Steps,
		Metadata:  def.Metadata,
		Elapsed:   time.Since(start),
	}
}

// Bucket returns a deterministic 0-99 bucket for a user ID.
// The same user ID always gets the same bucket.
func Bucket(userID string) int {
	return govern.Bucket(userID)
}

// Handler returns an HTTP handler that serves flag state as JSON.
func (f *Flags) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/flags", func(w http.ResponseWriter, r *http.Request) {
		ctx := buildHTTPContext(r)
		flags := f.AllFlags(ctx)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(flags)
	})

	mux.HandleFunc("/explain", func(w http.ResponseWriter, r *http.Request) {
		ctx := buildHTTPContext(r)
		flagName := r.URL.Query().Get("flag")
		if flagName == "" {
			http.Error(w, "missing flag parameter", http.StatusBadRequest)
			return
		}
		eval := f.Explain(flagName, ctx)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(eval)
	})

	return mux
}

func (f *Flags) evalVariantName(def *FlagDef, rc *govern.RequestCache, trace *govern.Arbitrace, bundleID string, view overrides.View) string {
	if cached, ok := rc.FlagVariant(def.Key); ok {
		return cached
	}

	result := def.Default
	defer func() {
		rc.RecordFlagResult(def.Key, result, def.Default)
	}()

	if rc.HasCycle(def.Key) {
		trace.AppendScoped(govern.ArbitracePhaseGovernance, govern.ArbitraceScopeFlag, def.Key, govern.ArbitraceKindCycle, def.Key, "cycle detection", false,
			fmt.Sprintf("prerequisite cycle detected involving %s", def.Key))
		return result
	}

	rc.Enter(def.Key)
	defer rc.Leave(def.Key)

	if f.effectiveFlagKillSwitch(def, bundleID, view).RecordScoped(trace, govern.ArbitraceScopeFlag, def.Key, "kill_switch") {
		return result
	}

	if !f.prerequisitesMet(def, rc, trace, bundleID, view) {
		return result
	}

	for i, rule := range def.Rules {
		if !f.ruleMatches(def.Key, i, rule, rc, trace) {
			continue
		}
		if !f.rolloutAllows(def.Key, i, rule, rc, trace, bundleID, view) {
			continue
		}
		if variant, ok := f.assignVariant(def.Key, i, rule, rc, trace, bundleID); ok {
			result = variant
		} else {
			result = rule.Variant
		}
		return result
	}

	return result
}

func (f *Flags) prerequisitesMet(def *FlagDef, rc *govern.RequestCache, trace *govern.Arbitrace, bundleID string, view overrides.View) bool {
	for _, prereq := range def.Prerequisites {
		passed := f.prerequisitePassed(prereq, rc, bundleID, view)
		trace.AppendScoped(govern.ArbitracePhaseGovernance, govern.ArbitraceScopeFlag, def.Key, govern.ArbitraceKindRequires, prereq, "", passed, fmt.Sprintf("%s -> %v", prereq, passed))
		if !passed {
			return false
		}
	}
	return true
}

func (f *Flags) prerequisitePassed(name string, rc *govern.RequestCache, bundleID string, view overrides.View) bool {
	f.mu.RLock()
	prereqDef, ok := f.defs[name]
	f.mu.RUnlock()
	if !ok {
		return rc.PrerequisiteMet(name)
	}

	if cached, ok := rc.FlagVariant(name); ok {
		return cached != prereqDef.Default
	}

	prereqVariant := f.evalVariantName(prereqDef, rc, nil, bundleID, view)
	return prereqVariant != prereqDef.Default
}

func (f *Flags) ruleMatches(flagKey string, ruleIndex int, rule FlagRule, rc *govern.RequestCache, trace *govern.Arbitrace) bool {
	subject := flagRuleSubject(flagKey, ruleIndex)
	if !govern.RecordActiveWindow(trace, rc.EvalTime(), govern.ArbitraceScopeFlagRule, subject, "", rule.HasActiveFrom, rule.ActiveFromUnixNano, rule.HasActiveUntil, rule.ActiveUntilUnixNano) {
		return false
	}
	if rule.SegmentName != "" {
		// Flag evaluation always resolves to a variant (fail-closed to the
		// default), so a segment runtime error is surfaced in the trace
		// detail rather than as a Go error; it still yields segOK=false.
		segOK, detail, _ := rc.EvalSegment(rule.SegmentName)
		trace.AppendScoped(govern.ArbitracePhaseMatch, govern.ArbitraceScopeFlagRule, subject, govern.ArbitraceKindSegment, rule.SegmentName, "", segOK, detail)
		if !segOK {
			return false
		}
	}
	if rule.CompiledInline != nil {
		matched, err := rule.CompiledInline.Eval(rc.NestedContext())
		detail := fmt.Sprintf("%s -> %v", rule.InlineExpr, matched)
		if err != nil {
			detail = fmt.Sprintf("%s error: %v", rule.InlineExpr, err)
		}
		trace.AppendScoped(govern.ArbitracePhaseMatch, govern.ArbitraceScopeFlagRule, subject, govern.ArbitraceKindCondition, "", "inline condition", matched, detail)
		return matched
	}
	if rule.SegmentName == "" {
		trace.AppendScoped(govern.ArbitracePhaseMatch, govern.ArbitraceScopeFlagRule, subject, govern.ArbitraceKindCondition, "", "inline condition", false, "no segment or inline condition")
		return false
	}
	return true
}

func (f *Flags) rolloutAllows(flagKey string, ruleIndex int, rule FlagRule, rc *govern.RequestCache, trace *govern.Arbitrace, bundleID string, view overrides.View) bool {
	spec := f.effectiveRuleRollout(flagKey, ruleIndex, rule, bundleID, view)
	if spec == nil {
		return true
	}
	decision := govern.DecidePercentRollout(*spec, rc.Context())
	trace.AppendScoped(govern.ArbitracePhaseGovernance, govern.ArbitraceScopeFlagRule, flagRuleSubject(flagKey, ruleIndex), govern.ArbitraceKindRollout, spec.Namespace, spec.CheckLabel(), decision.Allowed, decision.Detail())
	return decision.Allowed
}

func (f *Flags) assignVariant(flagKey string, ruleIndex int, rule FlagRule, rc *govern.RequestCache, trace *govern.Arbitrace, bundleID string) (string, bool) {
	if len(rule.Split) == 0 {
		return "", false
	}
	subject := rule.SplitSubject
	if subject == "" {
		subject = govern.DefaultRolloutSubject
	}
	namespace := rule.SplitNamespace
	if namespace == "" {
		namespace = govern.AutoRolloutNamespace(bundleID, fmt.Sprintf("flag:%s:rule:%d:split", flagKey, ruleIndex))
	}
	subjectKey := flagRuleSubject(flagKey, ruleIndex)
	subjectValue, ok := govern.RolloutSubject(rc.Context(), subject)
	if !ok {
		trace.AppendScoped(
			govern.ArbitracePhaseEffect,
			govern.ArbitraceScopeFlagRule,
			subjectKey,
			govern.ArbitraceKindSplit,
			namespace,
			fmt.Sprintf(`split by %s namespace %q`, subject, namespace),
			false,
			fmt.Sprintf("subject_key=%s missing, resolution=%d", subject, govern.RolloutResolution),
		)
		return "", false
	}
	bucket := govern.RolloutBucket(namespace, subjectValue)
	var (
		assigned string
		bands    strings.Builder
		start    uint16
	)
	for i, band := range rule.Split {
		end := start + band.WeightBps - 1
		if i > 0 {
			bands.WriteString(",")
		}
		bands.WriteString(fmt.Sprintf("%s:%d-%d", band.Variant, start, end))
		if assigned == "" && bucket < start+band.WeightBps {
			assigned = band.Variant
		}
		start += band.WeightBps
	}
	trace.AppendScoped(
		govern.ArbitracePhaseEffect,
		govern.ArbitraceScopeFlagRule,
		subjectKey,
		govern.ArbitraceKindSplit,
		assigned,
		fmt.Sprintf(`split by %s namespace %q`, subject, namespace),
		true,
		fmt.Sprintf(
			`subject_key=%s, subject=%q, namespace=%q, bucket=%d, assigned=%s, bands={%s}`,
			subject,
			subjectValue,
			namespace,
			bucket,
			assigned,
			bands.String(),
		),
	)
	return assigned, true
}

func (f *Flags) effectiveFlagKillSwitch(def *FlagDef, bundleID string, view overrides.View) govern.KillSwitchDecision {
	var override *bool
	if view != nil {
		if ov, ok := view.Flag(bundleID, def.Key); ok {
			override = ov.KillSwitch
		}
	}
	return govern.ResolveKillSwitch(def.KillSwitch.IsSet(), def.KillSwitch.Enabled(), override)
}

func (f *Flags) effectiveRuleRollout(flagKey string, ruleIndex int, rule FlagRule, bundleID string, view overrides.View) *govern.PercentRollout {
	var rolloutBps uint16
	hasRollout := rule.HasRollout
	if hasRollout {
		rolloutBps = rule.RolloutBps
	}
	if view != nil {
		if ov, ok := view.FlagRule(bundleID, flagKey, ruleIndex); ok && ov.Rollout != nil {
			hasRollout = true
			rolloutBps = *ov.Rollout
		}
	}
	if !hasRollout {
		return nil
	}
	subject := rule.RolloutSubject
	if subject == "" {
		subject = govern.DefaultRolloutSubject
	}
	namespace := rule.RolloutNamespace
	if namespace == "" {
		namespace = govern.AutoRolloutNamespace(bundleID, fmt.Sprintf("flag:%s:rule:%d", flagKey, ruleIndex))
	}
	return &govern.PercentRollout{
		PercentBps: rolloutBps,
		SubjectKey: subject,
		Namespace:  namespace,
	}
}

func buildHTTPContext(r *http.Request) map[string]any {
	ctx := make(map[string]any)
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			val := values[0]
			if n, err := strconv.ParseFloat(val, 64); err == nil {
				ctx[key] = n
			} else {
				ctx[key] = val
			}
		}
	}
	if userID, ok := ctx["user_id"].(string); ok {
		ctx["percent_bucket"] = float64(Bucket(userID))
	}
	return ctx
}

func buildReason(def *FlagDef, variant string, trace []ArbitraceStep) string {
	if variant == def.Default {
		killSwitchDisabled := ""
		for _, step := range trace {
			if step.Kind == govern.ArbitraceKindKillSwitch && step.Result {
				return "kill-switched"
			}
			if step.Kind == govern.ArbitraceKindKillSwitch && !step.Result && killSwitchDisabled == "" {
				killSwitchDisabled = step.Detail
			}
			if step.Kind == govern.ArbitraceKindCycle && !step.Result {
				return "prerequisite cycle detected"
			}
			if step.Kind == govern.ArbitraceKindRequires && !step.Result {
				return fmt.Sprintf("prerequisite %s not met", step.Target)
			}
			if step.Kind == govern.ArbitraceKindActiveFrom && !step.Result {
				return "before active_from"
			}
			if step.Kind == govern.ArbitraceKindActiveUntil && !step.Result {
				return "after active_until"
			}
		}
		if killSwitchDisabled != "" {
			return killSwitchDisabled + "; no rules matched"
		}
		return "no rules matched"
	}
	for _, step := range trace {
		if step.Kind == govern.ArbitraceKindSplit && step.Result {
			return "assigned by split"
		}
		if step.Kind == govern.ArbitraceKindSegment && step.Result {
			return fmt.Sprintf("matched segment %s", step.Target)
		}
		if step.Kind == govern.ArbitraceKindCondition && step.Result {
			return "matched inline condition"
		}
	}
	return fmt.Sprintf("variant: %s", variant)
}

func flagRuleSubject(flagKey string, ruleIndex int) string {
	return fmt.Sprintf("%s[%d]", flagKey, ruleIndex)
}
