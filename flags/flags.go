package flags

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"

	"github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/internal/parseutil"
	"github.com/odvcencio/arbiter/overrides"
)

// Flags is a compiled flag ruleset with first-class flag concepts and rich explainability.
type Flags struct {
	mu       sync.RWMutex
	defs     map[string]*FlagDef
	segments *govern.SegmentSet
	source   []byte
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
	full, err := arbiter.CompileFullParsed(parsed)
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
// Looks for flags/<env>.arb at the given base directory.
func LoadEnv(dir, env string) (*Flags, error) {
	path := dir + "/" + env + ".arb"
	return LoadFile(path)
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

// Explain evaluates a flag and returns a rich trace of the evaluation.
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

	trace := &govern.Trace{}
	rc := govern.NewRequestCache(f.segments, ctx)
	name := f.evalVariantName(def, rc, trace, "", nil)

	reason := buildReason(def, name, trace.Steps)

	return FlagEvaluation{
		Flag:      flag,
		Variant:   f.resolveVariantRedacted(def, name),
		IsDefault: name == def.Default,
		Reason:    reason,
		Trace:     trace.Steps,
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

	trace := &govern.Trace{}
	rc := govern.NewRequestCache(f.segments, ctx)
	name := f.evalVariantName(def, rc, trace, bundleID, view)

	return FlagEvaluation{
		Flag:      flag,
		Variant:   f.resolveVariantRedacted(def, name),
		IsDefault: name == def.Default,
		Reason:    buildReason(def, name, trace.Steps),
		Trace:     trace.Steps,
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

func (f *Flags) evalVariantName(def *FlagDef, rc *govern.RequestCache, trace *govern.Trace, bundleID string, view overrides.View) string {
	if cached, ok := rc.FlagVariant(def.Key); ok {
		return cached
	}

	result := def.Default
	defer func() {
		rc.RecordFlagResult(def.Key, result, def.Default)
	}()

	if rc.HasCycle(def.Key) {
		trace.Append("cycle detection", false,
			fmt.Sprintf("prerequisite cycle detected involving %s", def.Key))
		return result
	}

	rc.Enter(def.Key)
	defer rc.Leave(def.Key)

	if govern.IsKillSwitched(f.effectiveFlagKillSwitch(def, bundleID, view), trace) {
		return result
	}

	if !f.prerequisitesMet(def, rc, trace, bundleID, view) {
		return result
	}

	for i, rule := range def.Rules {
		if !f.ruleMatches(def.Key, i, rule, rc, trace, bundleID, view) {
			continue
		}
		if !f.rolloutAllows(def.Key, i, rule, rc, trace, bundleID, view) {
			continue
		}
		result = rule.Variant
		return result
	}

	return result
}

func (f *Flags) prerequisitesMet(def *FlagDef, rc *govern.RequestCache, trace *govern.Trace, bundleID string, view overrides.View) bool {
	for _, prereq := range def.Prerequisites {
		passed := f.prerequisitePassed(prereq, rc, bundleID, view)
		trace.Append("requires "+prereq, passed, fmt.Sprintf("%s -> %v", prereq, passed))
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

func (f *Flags) ruleMatches(flagKey string, ruleIndex int, rule FlagRule, rc *govern.RequestCache, trace *govern.Trace, bundleID string, view overrides.View) bool {
	matched, detail := f.ruleMatchDetail(rule, rc)
	checkName := "segment " + rule.SegmentName
	if rule.SegmentName == "" {
		checkName = "inline condition"
	}
	rollout := f.effectiveRuleRollout(flagKey, ruleIndex, rule, bundleID, view)
	if rollout > 0 {
		checkName += fmt.Sprintf(" rollout %d%%", rollout)
	}
	trace.Append(checkName, matched, detail)
	return matched
}

func (f *Flags) ruleMatchDetail(rule FlagRule, rc *govern.RequestCache) (bool, string) {
	if rule.SegmentName != "" && rule.CompiledInline != nil {
		// Segment + inline combo: both must match
		segOk, segDetail := rc.EvalSegment(rule.SegmentName)
		if !segOk {
			return false, segDetail
		}
		inlineOk := rule.CompiledInline.Eval(rc.NestedContext())
		return inlineOk, fmt.Sprintf("segment %s (%s) and %s -> %v", rule.SegmentName, segDetail, rule.InlineExpr, inlineOk)
	}
	if rule.SegmentName != "" {
		return rc.EvalSegment(rule.SegmentName)
	}
	if rule.CompiledInline != nil {
		matched := rule.CompiledInline.Eval(rc.NestedContext())
		return matched, fmt.Sprintf("%s -> %v", rule.InlineExpr, matched)
	}
	return false, "no segment or inline condition"
}

func (f *Flags) rolloutAllows(flagKey string, ruleIndex int, rule FlagRule, rc *govern.RequestCache, trace *govern.Trace, bundleID string, view overrides.View) bool {
	rollout := f.effectiveRuleRollout(flagKey, ruleIndex, rule, bundleID, view)
	if rollout <= 0 {
		return true
	}
	userID := govern.RolloutUserID(rc.Context())
	bucket := govern.Bucket(userID)
	allowed := bucket < rollout
	trace.Append(
		fmt.Sprintf("rollout %d%%", rollout),
		allowed,
		fmt.Sprintf("bucket(%q) = %d, threshold = %d", userID, bucket, rollout),
	)
	return allowed
}

func (f *Flags) effectiveFlagKillSwitch(def *FlagDef, bundleID string, view overrides.View) bool {
	if view == nil {
		return def.KillSwitch
	}
	if ov, ok := view.Flag(bundleID, def.Key); ok && ov.KillSwitch != nil {
		return *ov.KillSwitch
	}
	return def.KillSwitch
}

func (f *Flags) effectiveRuleRollout(flagKey string, ruleIndex int, rule FlagRule, bundleID string, view overrides.View) int {
	if view == nil {
		return rule.Rollout
	}
	if ov, ok := view.FlagRule(bundleID, flagKey, ruleIndex); ok && ov.Rollout != nil {
		return int(*ov.Rollout)
	}
	return rule.Rollout
}

func compileInlineSegment(conditionSource string) (*govern.CompiledSegment, error) {
	syntheticSource := fmt.Sprintf("rule __inline { when { %s } then Match {} }", conditionSource)
	rs, err := arbiter.Compile([]byte(syntheticSource))
	if err != nil {
		return nil, fmt.Errorf("compile inline condition: %w", err)
	}
	return &govern.CompiledSegment{
		Name:    "inline",
		Source:  conditionSource,
		Ruleset: rs,
	}, nil
}

// --- CST parsing ---

func (f *Flags) parseParsed(parsed *arbiter.ParsedSource, full *arbiter.CompileResult) error {
	if parsed == nil {
		return fmt.Errorf("nil parsed source")
	}
	if full == nil {
		return fmt.Errorf("nil compiled ruleset")
	}
	source := parsed.Source
	lang := parsed.Lang
	root := parsed.Root
	f.defs = make(map[string]*FlagDef)
	f.segments = full.Segments
	f.source = source

	// Walk the CST
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		nodeType := child.Type(lang)

		switch nodeType {
		case "flag_declaration":
			def, err := parseFlag(child, source, lang)
			if err != nil {
				return fmt.Errorf("parse flag: %w", err)
			}
			f.defs[def.Key] = def
		}
	}

	return nil
}

func parseFlag(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (*FlagDef, error) {
	def := &FlagDef{}

	// Name
	if nameNode := node.ChildByFieldName("name", lang); nameNode != nil {
		def.Key = nodeText(nameNode, source)
	}

	// Type
	if typeNode := node.ChildByFieldName("flag_type", lang); typeNode != nil {
		switch nodeText(typeNode, source) {
		case "boolean":
			def.Type = FlagBoolean
		case "multivariate":
			def.Type = FlagMultivariate
		}
	}

	// Default
	if defaultNode := node.ChildByFieldName("default_value", lang); defaultNode != nil {
		def.Default = parseutil.StripQuotes(nodeText(defaultNode, source))
	}

	// Kill switch
	if ksNode := node.ChildByFieldName("kill_switch", lang); ksNode != nil {
		def.KillSwitch = true
	}

	// Walk body children for metadata, requires, rules
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		childType := child.Type(lang)

		switch childType {
		case "flag_metadata":
			key := ""
			value := ""
			if keyNode := child.ChildByFieldName("key", lang); keyNode != nil {
				key = nodeText(keyNode, source)
			}
			if valNode := child.ChildByFieldName("value", lang); valNode != nil {
				value = parseutil.StripQuotes(nodeText(valNode, source))
			}
			switch key {
			case "owner":
				def.Metadata.Owner = value
			case "ticket":
				def.Metadata.Ticket = value
			case "rationale":
				def.Metadata.Rationale = value
			case "expires":
				if t, err := time.Parse("2006-01-02", value); err == nil {
					def.Metadata.Expires = &t
				}
			}

		case "flag_requires":
			if fnNode := child.ChildByFieldName("flag_name", lang); fnNode != nil {
				def.Prerequisites = append(def.Prerequisites, nodeText(fnNode, source))
			}

		case "flag_rule":
			rule, err := parseFlagRule(child, source, lang)
			if err != nil {
				return nil, err
			}
			def.Rules = append(def.Rules, rule)

		case "variant_declaration":
			vd := parseVariantDecl(child, source, lang)
			if def.Variants == nil {
				def.Variants = make(map[string]*VariantDef)
			}
			def.Variants[vd.Name] = vd

		case "defaults_block":
			def.DefaultValues = parseParamBlock(child, source, lang)
		}
	}

	// Load-time schema inference: check type consistency across variants
	if len(def.Variants) > 0 {
		def.Schema = make(map[string]ValueType)

		// Collect types from defaults
		for k, v := range def.DefaultValues {
			def.Schema[k] = inferType(v)
		}

		// Check each variant's values against schema
		for vname, vd := range def.Variants {
			for k, v := range vd.Values {
				vt := inferType(v)
				if existing, ok := def.Schema[k]; ok {
					if existing != vt {
						return nil, fmt.Errorf("flag %s: variant %q field %q has type %s, expected %s (from prior declaration)",
							def.Key, vname, k, typeName(vt), typeName(existing))
					}
				} else {
					def.Schema[k] = vt
				}
			}
		}
	}

	// Load-time validation: every 'then "X"' must reference a declared variant
	// (only if variants are declared — undeclared-name flags skip this check)
	if len(def.Variants) > 0 {
		for _, rule := range def.Rules {
			if _, ok := def.Variants[rule.Variant]; !ok {
				return nil, fmt.Errorf("flag %s: rule references undeclared variant %q (declared: %v)",
					def.Key, rule.Variant, variantNames(def.Variants))
			}
		}
		// Default must also be declared
		if _, ok := def.Variants[def.Default]; !ok {
			// Auto-declare an empty default variant
			def.Variants[def.Default] = &VariantDef{Name: def.Default}
		}
	}

	return def, nil
}

func parseVariantDecl(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) *VariantDef {
	vd := &VariantDef{}
	if nameNode := node.ChildByFieldName("name", lang); nameNode != nil {
		vd.Name = parseutil.StripQuotes(nodeText(nameNode, source))
	}
	vd.Values = parseParamBlock(node, source, lang)
	return vd
}

func parseParamBlock(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) map[string]any {
	values := make(map[string]any)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type(lang) == "param_assignment" {
			key := ""
			if keyNode := child.ChildByFieldName("key", lang); keyNode != nil {
				key = nodeText(keyNode, source)
			}
			if valNode := child.ChildByFieldName("value", lang); valNode != nil {
				values[key] = parseConstValue(valNode, source, lang)
			}
		}
	}
	return values
}

func parseConstValue(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) any {
	text := nodeText(node, source)
	switch node.Type(lang) {
	case "number_literal":
		n, _ := strconv.ParseFloat(text, 64)
		return n
	case "string_literal":
		return parseutil.StripQuotes(text)
	case "bool_literal":
		return text == "true"
	case "secret_ref":
		refNode := node.ChildByFieldName("ref", lang)
		if refNode != nil {
			return SecretValue{Ref: parseutil.StripQuotes(nodeText(refNode, source))}
		}
		return SecretValue{Ref: ""}
	default:
		return parseutil.StripQuotes(text)
	}
}

func inferType(v any) ValueType {
	switch v.(type) {
	case string:
		return ValueString
	case float64:
		return ValueNumber
	case bool:
		return ValueBool
	case SecretValue:
		return ValueSecret
	default:
		return ValueUnknown
	}
}

func typeName(vt ValueType) string {
	switch vt {
	case ValueString:
		return "string"
	case ValueNumber:
		return "number"
	case ValueBool:
		return "bool"
	case ValueSecret:
		return "secret"
	default:
		return "unknown"
	}
}

func variantNames(variants map[string]*VariantDef) []string {
	names := make([]string, 0, len(variants))
	for k := range variants {
		names = append(names, k)
	}
	return names
}

func parseFlagRule(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (FlagRule, error) {
	rule := FlagRule{}

	// Condition: segment ref, inline { expr }, or segment + inline { expr }
	condNode := node.ChildByFieldName("condition", lang)

	// Check for segment field (segment + inline combo).
	// Fields from anonymous sequences inside Choice bubble up to the parent rule node.
	if segNode := node.ChildByFieldName("segment", lang); segNode != nil {
		rule.SegmentName = nodeText(segNode, source)
	}

	if condNode != nil && condNode.Type(lang) == "identifier" && rule.SegmentName == "" {
		// Segment reference only
		rule.SegmentName = nodeText(condNode, source)
	} else {
		// Inline condition: find the expression node between braces.
		variantNode := node.ChildByFieldName("variant", lang)
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if variantNode != nil && child.StartByte() == variantNode.StartByte() && child.EndByte() == variantNode.EndByte() {
				continue
			}
			childType := child.Type(lang)
			if childType == "number_literal" || childType == "identifier" {
				continue
			}
			rule.InlineExpr = strings.TrimSpace(nodeText(child, source))
			break
		}
		// Precompile inline condition at load time (not eval time)
		if rule.InlineExpr != "" {
			compiled, err := compileInlineSegment(rule.InlineExpr)
			if err != nil {
				return rule, err
			}
			rule.CompiledInline = compiled
		}
	}

	// Rollout
	if rolloutNode := node.ChildByFieldName("rollout", lang); rolloutNode != nil {
		rolloutText := nodeText(rolloutNode, source)
		rule.Rollout = parseutil.ParseInt(rolloutText)
	}

	// Variant
	if variantNode := node.ChildByFieldName("variant", lang); variantNode != nil {
		variantText := nodeText(variantNode, source)
		rule.Variant = parseutil.StripQuotes(variantText)
	}

	return rule, nil
}

// --- Helpers ---

func nodeText(n *gotreesitter.Node, source []byte) string {
	return string(source[n.StartByte():n.EndByte()])
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

func buildReason(def *FlagDef, variant string, trace []TraceStep) string {
	if variant == def.Default {
		for _, step := range trace {
			if step.Check == "kill_switch" && step.Result {
				return "kill-switched"
			}
			if step.Check == "cycle detection" && !step.Result {
				return "prerequisite cycle detected"
			}
			if strings.HasPrefix(step.Check, "requires ") && !step.Result {
				return fmt.Sprintf("prerequisite %s not met", strings.TrimPrefix(step.Check, "requires "))
			}
		}
		return "no rules matched"
	}
	for _, step := range trace {
		if strings.HasPrefix(step.Check, "segment ") && step.Result {
			return fmt.Sprintf("matched %s", step.Check)
		}
		if step.Check == "inline condition" && step.Result {
			return "matched inline condition"
		}
	}
	return fmt.Sprintf("variant: %s", variant)
}
