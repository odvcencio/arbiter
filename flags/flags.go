package flags

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"

	"github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/vm"
)

// Flags is a compiled flag ruleset with first-class flag concepts and rich explainability.
type Flags struct {
	mu       sync.RWMutex
	defs     map[string]*FlagDef        // flag key -> definition
	segments map[string]*compiledSegment // segment name -> compiled condition
	source   []byte                     // original source for reload
}

type compiledSegment struct {
	name    string
	source  string                     // original condition text
	ruleset *compiler.CompiledRuleset  // compiled condition bytecode
}

// Load parses .arb source, extracts flags + segments, and compiles segments.
func Load(source []byte) (*Flags, error) {
	f := &Flags{}
	if err := f.parse(source); err != nil {
		return nil, err
	}
	return f, nil
}


// LoadFile loads and compiles flags from a file path.
func LoadFile(path string) (*Flags, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flags file: %w", err)
	}
	return Load(source)
}

// LoadEnv loads flags for a specific environment.
// Looks for flags/<env>.arb at the given base directory.
func LoadEnv(dir, env string) (*Flags, error) {
	path := dir + "/" + env + ".arb"
	return LoadFile(path)
}

// Reload atomically re-parses and swaps the flag definitions from new source.
func (f *Flags) Reload(source []byte) error {
	newF := &Flags{}
	if err := newF.parse(source); err != nil {
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
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read flags file: %w", err)
	}
	return f.Reload(source)
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
	rc := newRequestCache(f, ctx)
	v := rc.evalVariantName(def, nil)
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
	rc := newRequestCache(f, ctx)
	name := rc.evalVariantName(def, nil)
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
	rc := newRequestCache(f, ctx)
	return rc.evalVariantName(def, nil)
}

// AllFlags returns all flag variants for the given context.
func (f *Flags) AllFlags(ctx map[string]any) map[string]ServedVariant {
	f.mu.RLock()
	defs := f.defs
	f.mu.RUnlock()

	rc := newRequestCache(f, ctx)
	result := make(map[string]ServedVariant, len(defs))
	for key, def := range defs {
		name := rc.evalVariantName(def, nil)
		result[key] = f.resolveVariantRedacted(def, name)
	}
	return result
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
	segs := f.segments
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

	var trace []TraceStep
	rc := newRequestCache(f, ctx)
	name := rc.evalVariantName(def, &trace)

	reason := buildReason(def, name, trace, segs)

	return FlagEvaluation{
		Flag:      flag,
		Variant:   f.resolveVariantRedacted(def, name),
		IsDefault: name == def.Default,
		Reason:    reason,
		Trace:     trace,
		Metadata:  def.Metadata,
		Elapsed:   time.Since(start),
	}
}

// Bucket returns a deterministic 0-99 bucket for a user ID.
// The same user ID always gets the same bucket.
func Bucket(userID string) int {
	h := sha256.Sum256([]byte(userID))
	n := binary.BigEndian.Uint32(h[:4])
	return int(n % 100)
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

// --- Per-request cache ---
// Memoizes segment results, prerequisite results, and nested context
// so shared segments are only evaluated once per request.

type requestCache struct {
	flags      *Flags
	ctx        map[string]any
	nestedCtx  map[string]any       // computed once
	segResults map[string]bool      // segment name → result
	prereqResults map[string]string // flag key → variant (for cycle detection + memo)
	evalStack  map[string]bool      // flags currently being evaluated (cycle detection)
}

func newRequestCache(f *Flags, ctx map[string]any) *requestCache {
	return &requestCache{
		flags:         f,
		ctx:           ctx,
		nestedCtx:     nestDottedKeys(ctx),
		segResults:    make(map[string]bool),
		prereqResults: make(map[string]string),
		evalStack:     make(map[string]bool),
	}
}

// evalVariant implements the core evaluation logic with memoization and cycle detection.
func (rc *requestCache) evalVariantName(def *FlagDef, trace *[]TraceStep) string {
	// Cycle detection
	if rc.evalStack[def.Key] {
		if trace != nil {
			*trace = append(*trace, TraceStep{
				Check:  "cycle detection",
				Result: false,
				Detail: fmt.Sprintf("prerequisite cycle detected involving %s", def.Key),
			})
		}
		return def.Default
	}
	rc.evalStack[def.Key] = true
	defer func() { delete(rc.evalStack, def.Key) }()

	// 1. Kill switch
	if def.KillSwitch {
		if trace != nil {
			*trace = append(*trace, TraceStep{
				Check:  "kill_switch",
				Result: true,
				Detail: "flag is kill-switched, returning default",
			})
		}
		return def.Default
	}

	// 2. Prerequisites
	for _, prereq := range def.Prerequisites {
		rc.flags.mu.RLock()
		prereqDef, ok := rc.flags.defs[prereq]
		rc.flags.mu.RUnlock()

		prereqPassed := false
		if ok {
			// Check memo first
			if cached, hasCached := rc.prereqResults[prereq]; hasCached {
				prereqPassed = cached != prereqDef.Default
			} else {
				prereqVariant := rc.evalVariantName(prereqDef, nil)
				rc.prereqResults[prereq] = prereqVariant
				prereqPassed = prereqVariant != prereqDef.Default
			}
		}

		if trace != nil {
			detail := fmt.Sprintf("%s -> %v", prereq, prereqPassed)
			*trace = append(*trace, TraceStep{
				Check:  fmt.Sprintf("requires %s", prereq),
				Result: prereqPassed,
				Detail: detail,
			})
		}

		if !prereqPassed {
			return def.Default
		}
	}

	// 3. Evaluate rules in order
	for _, rule := range def.Rules {
		segMatched := false
		var segDetail string

		if rule.SegmentName != "" {
			segMatched, segDetail = rc.evalSegmentCached(rule.SegmentName)
		} else if rule.CompiledInline != nil {
			segMatched = evalCompiledSegment(rule.CompiledInline, rc.nestedCtx)
			segDetail = fmt.Sprintf("%s -> %v", rule.InlineExpr, segMatched)
		}

		if trace != nil {
			checkName := "segment " + rule.SegmentName
			if rule.SegmentName == "" {
				checkName = "inline condition"
			}
			if rule.Rollout > 0 {
				checkName += fmt.Sprintf(" rollout %d%%", rule.Rollout)
			}
			*trace = append(*trace, TraceStep{
				Check:  checkName,
				Result: segMatched,
				Detail: segDetail,
			})
		}

		if !segMatched {
			continue
		}

		// Check rollout
		if rule.Rollout > 0 {
			userID := ""
			if uid, ok := rc.ctx["user.id"].(string); ok {
				userID = uid
			} else if uid, ok := rc.ctx["user_id"].(string); ok {
				userID = uid
			}
			bucket := Bucket(userID)
			rolloutPassed := bucket < rule.Rollout

			if trace != nil {
				*trace = append(*trace, TraceStep{
					Check:  fmt.Sprintf("rollout %d%%", rule.Rollout),
					Result: rolloutPassed,
					Detail: fmt.Sprintf("bucket(%q) = %d, threshold = %d", userID, bucket, rule.Rollout),
				})
			}

			if !rolloutPassed {
				continue
			}
		}

		return rule.Variant
	}

	// 4. No rules matched -> return default
	return def.Default
}

// evalSegmentCached evaluates a segment with memoization.
func (rc *requestCache) evalSegmentCached(name string) (bool, string) {
	if result, ok := rc.segResults[name]; ok {
		return result, fmt.Sprintf("%s -> %v (cached)", name, result)
	}

	rc.flags.mu.RLock()
	seg, ok := rc.flags.segments[name]
	rc.flags.mu.RUnlock()

	if !ok {
		rc.segResults[name] = false
		return false, fmt.Sprintf("segment %q not found", name)
	}

	matched := evalCompiledSegment(seg, rc.nestedCtx)
	rc.segResults[name] = matched
	return matched, fmt.Sprintf("%s -> %v", seg.source, matched)
}

// evalCompiledSegment evaluates a precompiled segment against a nested context.
func evalCompiledSegment(seg *compiledSegment, nestedCtx map[string]any) bool {
	sp := vm.NewStringPool(seg.ruleset.Constants.Strings())
	dc := vm.DataFromMap(nestedCtx, sp)
	matched, err := vm.EvalWithPool(seg.ruleset, dc, sp)
	if err != nil {
		return false
	}
	return len(matched) > 0
}

// --- Helpers ---

// nestDottedKeys converts a flat map with dotted keys into a nested map.
func nestDottedKeys(flat map[string]any) map[string]any {
	result := make(map[string]any)
	for k, v := range flat {
		parts := strings.Split(k, ".")
		if len(parts) == 1 {
			result[k] = v
			continue
		}
		current := result
		for i, part := range parts {
			if i == len(parts)-1 {
				current[part] = v
			} else {
				next, ok := current[part].(map[string]any)
				if !ok {
					next = make(map[string]any)
					current[part] = next
				}
				current = next
			}
		}
	}
	return result
}

func compileSegment(name, conditionSource string) (*compiledSegment, error) {
	syntheticSource := fmt.Sprintf("rule __seg_%s { when { %s } then Match {} }", name, conditionSource)
	rs, err := arbiter.Compile([]byte(syntheticSource))
	if err != nil {
		return nil, fmt.Errorf("compile segment %s: %w", name, err)
	}
	return &compiledSegment{
		name:    name,
		source:  conditionSource,
		ruleset: rs,
	}, nil
}

// --- CST parsing ---

func (f *Flags) parse(source []byte) error {
	lang, err := arbiter.GetLanguage()
	if err != nil {
		return fmt.Errorf("get language: %w", err)
	}

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	root := tree.RootNode()
	if root.HasError() {
		return fmt.Errorf("parse errors in arbiter source")
	}

	f.defs = make(map[string]*FlagDef)
	f.segments = make(map[string]*compiledSegment)
	f.source = source

	// Walk the CST
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		nodeType := child.Type(lang)

		switch nodeType {
		case "segment_declaration":
			seg, err := parseSegment(child, source, lang)
			if err != nil {
				return fmt.Errorf("parse segment: %w", err)
			}
			compiled, err := compileSegment(seg.Name, seg.Source)
			if err != nil {
				return fmt.Errorf("compile segment %s: %w", seg.Name, err)
			}
			f.segments[seg.Name] = compiled

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

func parseSegment(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (*Segment, error) {
	nameNode := node.ChildByFieldName("name", lang)
	condNode := node.ChildByFieldName("condition", lang)

	if nameNode == nil || condNode == nil {
		return nil, fmt.Errorf("segment missing name or condition")
	}

	name := nodeText(nameNode, source)
	condSource := strings.TrimSpace(nodeText(condNode, source))

	return &Segment{
		Name:   name,
		Source: condSource,
	}, nil
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
		def.Default = stripQuotes(nodeText(defaultNode, source))
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
				value = stripQuotes(nodeText(valNode, source))
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
		vd.Name = stripQuotes(nodeText(nameNode, source))
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
		return stripQuotes(text)
	case "bool_literal":
		return text == "true"
	case "secret_ref":
		refNode := node.ChildByFieldName("ref", lang)
		if refNode != nil {
			return SecretValue{Ref: stripQuotes(nodeText(refNode, source))}
		}
		return SecretValue{Ref: ""}
	default:
		return stripQuotes(text)
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

	// Condition: either identifier (segment ref) or inline { expr }
	condNode := node.ChildByFieldName("condition", lang)
	if condNode != nil && condNode.Type(lang) == "identifier" {
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
			if childType == "number_literal" {
				continue
			}
			rule.InlineExpr = strings.TrimSpace(nodeText(child, source))
			break
		}
		// Precompile inline condition at load time (not eval time)
		if rule.InlineExpr != "" {
			compiled, err := compileSegment("inline", rule.InlineExpr)
			if err != nil {
				return rule, fmt.Errorf("compile inline condition: %w", err)
			}
			rule.CompiledInline = compiled
		}
	}

	// Rollout
	if rolloutNode := node.ChildByFieldName("rollout", lang); rolloutNode != nil {
		rolloutText := nodeText(rolloutNode, source)
		n := 0
		for _, ch := range rolloutText {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int(ch-'0')
			}
		}
		rule.Rollout = n
	}

	// Variant
	if variantNode := node.ChildByFieldName("variant", lang); variantNode != nil {
		variantText := nodeText(variantNode, source)
		rule.Variant = stripQuotes(variantText)
	}

	return rule, nil
}

// --- Helpers ---

func nodeText(n *gotreesitter.Node, source []byte) string {
	return string(source[n.StartByte():n.EndByte()])
}

func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
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

func buildReason(def *FlagDef, variant string, trace []TraceStep, segs map[string]*compiledSegment) string {
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
