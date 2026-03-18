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

// Enabled returns true if the flag is enabled (non-default) for the given context.
func (f *Flags) Enabled(flag string, ctx map[string]any) bool {
	f.mu.RLock()
	def, ok := f.defs[flag]
	f.mu.RUnlock()
	if !ok {
		return false
	}
	v := f.evalVariant(def, ctx, nil)
	return v != def.Default
}

// Variant returns the variant string for a flag.
// Returns the default if no rules match, flag is kill-switched, or prerequisites fail.
func (f *Flags) Variant(flag string, ctx map[string]any) string {
	f.mu.RLock()
	def, ok := f.defs[flag]
	f.mu.RUnlock()
	if !ok {
		return ""
	}
	return f.evalVariant(def, ctx, nil)
}

// AllFlags returns all flag variants for the given context.
func (f *Flags) AllFlags(ctx map[string]any) map[string]string {
	f.mu.RLock()
	defs := f.defs
	f.mu.RUnlock()

	result := make(map[string]string, len(defs))
	for key, def := range defs {
		result[key] = f.evalVariant(def, ctx, nil)
	}
	return result
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
			Variant:   "",
			IsDefault: true,
			Reason:    "flag not found",
			Elapsed:   time.Since(start),
		}
	}

	var trace []TraceStep
	variant := f.evalVariant(def, ctx, &trace)

	reason := buildReason(def, variant, trace, segs)

	return FlagEvaluation{
		Flag:      flag,
		Variant:   variant,
		IsDefault: variant == def.Default,
		Reason:    reason,
		Trace:     trace,
		Metadata:  def.Metadata,
		Elapsed:   time.Since(start),
	}
}

// Bucket returns a deterministic 0-99 bucket for a user ID.
// Use this for percentage-based rollouts.
// The same user ID always gets the same bucket.
func Bucket(userID string) int {
	h := sha256.Sum256([]byte(userID))
	n := binary.BigEndian.Uint32(h[:4])
	return int(n % 100)
}

// Handler returns an HTTP handler that serves flag state as JSON.
// GET /flags?user_id=xxx&org=yyy&plan=zzz&env=production
// Response: {"new_dashboard": "treatment", "ai_features": "enabled"}
//
// GET /explain?flag=checkout_v2&user_id=xxx
// Response: FlagEvaluation JSON
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

// --- Internal evaluation ---

// evalVariant implements the core evaluation logic.
// If trace is non-nil, it records each evaluation step.
func (f *Flags) evalVariant(def *FlagDef, ctx map[string]any, trace *[]TraceStep) string {
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
		f.mu.RLock()
		prereqDef, ok := f.defs[prereq]
		f.mu.RUnlock()

		prereqPassed := false
		if ok {
			prereqVariant := f.evalVariant(prereqDef, ctx, nil)
			prereqPassed = prereqVariant != prereqDef.Default
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
	f.mu.RLock()
	segs := f.segments
	f.mu.RUnlock()

	for _, rule := range def.Rules {
		segMatched := false
		var segDetail string

		if rule.SegmentName != "" {
			seg, ok := segs[rule.SegmentName]
			if ok {
				segMatched = evalSegment(seg, ctx)
				segDetail = fmt.Sprintf("%s -> %v", seg.source, segMatched)
			} else {
				segDetail = fmt.Sprintf("segment %q not found", rule.SegmentName)
			}
		} else if rule.InlineExpr != "" {
			// Compile and evaluate inline expression
			inlineSeg, err := compileSegment("inline", rule.InlineExpr)
			if err == nil {
				segMatched = evalSegment(inlineSeg, ctx)
				segDetail = fmt.Sprintf("%s -> %v", rule.InlineExpr, segMatched)
			} else {
				segDetail = fmt.Sprintf("compile error: %v", err)
			}
		}

		if trace != nil {
			checkName := "segment " + rule.SegmentName
			if rule.SegmentName == "" {
				checkName = "inline condition"
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
			if uid, ok := ctx["user.id"].(string); ok {
				userID = uid
			} else if uid, ok := ctx["user_id"].(string); ok {
				userID = uid
			}
			bucket := Bucket(userID)
			rolloutPassed := bucket < rule.Rollout

			if trace != nil {
				*trace = append(*trace, TraceStep{
					Check:  fmt.Sprintf("rollout %d%%", rule.Rollout),
					Result: rolloutPassed,
					Detail: fmt.Sprintf("bucket(%s) = %d, threshold = %d", userID, bucket, rule.Rollout),
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

func evalSegment(seg *compiledSegment, ctx map[string]any) bool {
	nested := nestDottedKeys(ctx)
	sp := vm.NewStringPool(seg.ruleset.Constants.Strings())
	dc := vm.DataFromMap(nested, sp)
	matched, err := vm.EvalWithPool(seg.ruleset, dc, sp)
	if err != nil {
		return false
	}
	return len(matched) > 0
}

// nestDottedKeys converts a flat map with dotted keys (e.g. "user.plan" -> "enterprise")
// into a nested map (e.g. {"user": {"plan": "enterprise"}}).
// Non-dotted keys are preserved as-is.
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
		}
	}

	return def, nil
}

func parseFlagRule(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (FlagRule, error) {
	rule := FlagRule{}

	// Condition: either identifier (segment ref) or inline { expr }
	// The "condition" field may point to the identifier directly,
	// or to the "{" token when it's an inline condition.
	// We detect the pattern by checking if the condition field is an identifier.
	condNode := node.ChildByFieldName("condition", lang)
	if condNode != nil && condNode.Type(lang) == "identifier" {
		rule.SegmentName = nodeText(condNode, source)
	} else {
		// Inline condition: find the expression node between braces.
		// Walk all named children of the flag_rule node to find the expression
		// that isn't the variant node.
		variantNode := node.ChildByFieldName("variant", lang)
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if variantNode != nil && child.StartByte() == variantNode.StartByte() && child.EndByte() == variantNode.EndByte() {
				continue
			}
			childType := child.Type(lang)
			if childType == "number_literal" {
				// Could be rollout number, skip it
				// Check if preceding anonymous sibling is "rollout"
				continue
			}
			// This should be the inline expression
			rule.InlineExpr = strings.TrimSpace(nodeText(child, source))
			break
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
		// Find why it's the default
		for _, step := range trace {
			if step.Check == "kill_switch" && step.Result {
				return "kill-switched"
			}
			if strings.HasPrefix(step.Check, "requires ") && !step.Result {
				return fmt.Sprintf("prerequisite %s not met", strings.TrimPrefix(step.Check, "requires "))
			}
		}
		return "no rules matched"
	}
	// Find which rule matched
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
