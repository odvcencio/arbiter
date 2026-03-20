package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/vm"
)

type governedProgram struct {
	Path string
	Full *arbiter.CompileResult
}

type namedContext struct {
	Key     string         `json:"key"`
	Context map[string]any `json:"context"`
}

type ruleDelta struct {
	Name   string           `json:"name"`
	Before *audit.RuleMatch `json:"before,omitempty"`
	After  *audit.RuleMatch `json:"after,omitempty"`
}

type decisionDiff struct {
	Key       string            `json:"key"`
	Added     []audit.RuleMatch `json:"added,omitempty"`
	Removed   []audit.RuleMatch `json:"removed,omitempty"`
	Changed   []ruleDelta       `json:"changed,omitempty"`
	Unchanged bool              `json:"unchanged"`
}

type diffReport struct {
	BasePath      string         `json:"base_path"`
	CandidatePath string         `json:"candidate_path"`
	Compared      int            `json:"compared"`
	Changed       int            `json:"changed"`
	Unchanged     int            `json:"unchanged"`
	Differences   []decisionDiff `json:"differences,omitempty"`
}

type replayRecord struct {
	Key       string            `json:"key"`
	RequestID string            `json:"request_id,omitempty"`
	BundleID  string            `json:"bundle_id,omitempty"`
	Context   map[string]any    `json:"context"`
	Recorded  []audit.RuleMatch `json:"recorded,omitempty"`
}

type replayReport struct {
	RulesPath   string         `json:"rules_path"`
	AuditPath   string         `json:"audit_path"`
	Replayed    int            `json:"replayed"`
	Changed     int            `json:"changed"`
	Unchanged   int            `json:"unchanged"`
	Differences []decisionDiff `json:"differences,omitempty"`
}

func diffCmd(basePath, candidatePath, dataJSON, dataFile, keyPath string, jsonOut bool) error {
	if basePath == "" || candidatePath == "" {
		return fmt.Errorf("diff requires <base.arb> and <candidate.arb>")
	}
	contexts, err := loadNamedContexts(dataJSON, dataFile, keyPath)
	if err != nil {
		return err
	}
	base, err := loadGovernedProgram(basePath)
	if err != nil {
		return err
	}
	candidate, err := loadGovernedProgram(candidatePath)
	if err != nil {
		return err
	}

	report, err := diffGovernedPrograms(base, candidate, contexts)
	if err != nil {
		return err
	}
	return printDecisionReport(report, jsonOut)
}

func replayCmd(rulesPath, auditPath, requestID string, limit int, jsonOut bool) error {
	if rulesPath == "" {
		return fmt.Errorf("replay requires <rules.arb>")
	}
	if auditPath == "" {
		return fmt.Errorf("replay requires --audit <decisions.jsonl>")
	}
	program, err := loadGovernedProgram(rulesPath)
	if err != nil {
		return err
	}
	records, err := loadReplayRecords(auditPath, requestID, limit)
	if err != nil {
		return err
	}
	report, err := replayGovernedProgram(program, auditPath, records)
	if err != nil {
		return err
	}
	return printDecisionReport(report, jsonOut)
}

func loadGovernedProgram(path string) (governedProgram, error) {
	full, err := arbiter.CompileFullFile(path)
	if err != nil {
		return governedProgram{}, fmt.Errorf("compile %s: %w", path, err)
	}
	return governedProgram{Path: path, Full: full}, nil
}

func diffGovernedPrograms(base, candidate governedProgram, contexts []namedContext) (diffReport, error) {
	report := diffReport{
		BasePath:      base.Path,
		CandidatePath: candidate.Path,
		Compared:      len(contexts),
		Differences:   make([]decisionDiff, 0),
	}
	for _, item := range contexts {
		before, err := evaluateGoverned(base.Full, item.Context)
		if err != nil {
			return diffReport{}, fmt.Errorf("evaluate %s (%s): %w", base.Path, item.Key, err)
		}
		after, err := evaluateGoverned(candidate.Full, item.Context)
		if err != nil {
			return diffReport{}, fmt.Errorf("evaluate %s (%s): %w", candidate.Path, item.Key, err)
		}
		diff := compareRuleMatches(item.Key, before, after)
		if diff.Unchanged {
			report.Unchanged++
			continue
		}
		report.Changed++
		report.Differences = append(report.Differences, diff)
	}
	return report, nil
}

func replayGovernedProgram(program governedProgram, auditPath string, records []replayRecord) (replayReport, error) {
	report := replayReport{
		RulesPath:   program.Path,
		AuditPath:   auditPath,
		Replayed:    len(records),
		Differences: make([]decisionDiff, 0),
	}
	for _, record := range records {
		after, err := evaluateGoverned(program.Full, record.Context)
		if err != nil {
			return replayReport{}, fmt.Errorf("replay %s (%s): %w", program.Path, record.Key, err)
		}
		diff := compareRuleMatches(record.Key, record.Recorded, after)
		if diff.Unchanged {
			report.Unchanged++
			continue
		}
		report.Changed++
		report.Differences = append(report.Differences, diff)
	}
	return report, nil
}

func evaluateGoverned(full *arbiter.CompileResult, ctx map[string]any) ([]audit.RuleMatch, error) {
	if full == nil || full.Ruleset == nil {
		return nil, fmt.Errorf("nil compile result")
	}
	dc := arbiter.DataFromMap(ctx, full.Ruleset)
	matched, _, err := arbiter.EvalGoverned(full.Ruleset, dc, full.Segments, ctx)
	if err != nil {
		return nil, err
	}
	return toAuditRuleMatches(matched), nil
}

func toAuditRuleMatches(matched []vm.MatchedRule) []audit.RuleMatch {
	out := make([]audit.RuleMatch, 0, len(matched))
	for _, match := range matched {
		out = append(out, normalizeRuleMatch(audit.RuleMatch{
			Name:     match.Name,
			Priority: match.Priority,
			Action:   match.Action,
			Params:   cloneParams(match.Params),
			Fallback: match.Fallback,
		}))
	}
	return out
}

func compareRuleMatches(key string, before, after []audit.RuleMatch) decisionDiff {
	beforeByName := ruleMatchIndex(before)
	afterByName := ruleMatchIndex(after)
	names := make([]string, 0, len(beforeByName)+len(afterByName))
	for name := range beforeByName {
		names = append(names, name)
	}
	for name := range afterByName {
		if _, ok := beforeByName[name]; ok {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	diff := decisionDiff{Key: key, Unchanged: true}
	for _, name := range names {
		prev, hadPrev := beforeByName[name]
		next, hadNext := afterByName[name]
		switch {
		case hadPrev && !hadNext:
			diff.Removed = append(diff.Removed, prev)
			diff.Unchanged = false
		case !hadPrev && hadNext:
			diff.Added = append(diff.Added, next)
			diff.Unchanged = false
		case !sameRuleMatch(prev, next):
			prevCopy := prev
			nextCopy := next
			diff.Changed = append(diff.Changed, ruleDelta{Name: name, Before: &prevCopy, After: &nextCopy})
			diff.Unchanged = false
		}
	}
	return diff
}

func ruleMatchIndex(matches []audit.RuleMatch) map[string]audit.RuleMatch {
	index := make(map[string]audit.RuleMatch, len(matches))
	for _, match := range matches {
		index[match.Name] = normalizeRuleMatch(match)
	}
	return index
}

func sameRuleMatch(a, b audit.RuleMatch) bool {
	a = normalizeRuleMatch(a)
	b = normalizeRuleMatch(b)
	return a.Name == b.Name &&
		a.Priority == b.Priority &&
		a.Action == b.Action &&
		a.Fallback == b.Fallback &&
		stableJSON(a.Params) == stableJSON(b.Params)
}

func loadNamedContexts(dataJSON, dataFile, keyPath string) ([]namedContext, error) {
	if dataJSON == "" && dataFile == "" {
		return nil, fmt.Errorf("diff requires --data '{...}' or --data-file <contexts.json>")
	}
	if dataJSON != "" && dataFile != "" {
		return nil, fmt.Errorf("use either --data or --data-file, not both")
	}
	var raw []byte
	var err error
	if dataFile != "" {
		raw, err = os.ReadFile(dataFile)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", dataFile, err)
		}
	} else {
		raw = []byte(dataJSON)
	}
	return parseNamedContexts(raw, keyPath)
}

func parseNamedContexts(raw []byte, keyPath string) ([]namedContext, error) {
	var batch []map[string]any
	if err := json.Unmarshal(raw, &batch); err == nil {
		return normalizeNamedContexts(batch, keyPath), nil
	}

	var single map[string]any
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, fmt.Errorf("parse contexts: %w", err)
	}
	return normalizeNamedContexts([]map[string]any{single}, keyPath), nil
}

func normalizeNamedContexts(batch []map[string]any, keyPath string) []namedContext {
	out := make([]namedContext, 0, len(batch))
	for i, ctx := range batch {
		out = append(out, namedContext{
			Key:     contextKey(ctx, keyPath, i),
			Context: cloneAnyMap(ctx),
		})
	}
	return out
}

func contextKey(ctx map[string]any, keyPath string, index int) string {
	if keyPath != "" {
		if value, ok := lookupPath(ctx, keyPath); ok {
			return fmt.Sprint(value)
		}
	}
	for _, candidate := range []string{"request_id", "id", "key"} {
		if value, ok := lookupPath(ctx, candidate); ok {
			return fmt.Sprint(value)
		}
	}
	return fmt.Sprintf("context[%d]", index)
}

func lookupPath(ctx map[string]any, path string) (any, bool) {
	current := any(ctx)
	for _, segment := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := m[segment]
		if !ok {
			return nil, false
		}
		current = value
	}
	return current, true
}

func loadReplayRecords(path, requestID string, limit int) ([]replayRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	records := make([]replayRecord, 0)
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event audit.DecisionEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("parse audit line: %w", err)
		}
		if event.Kind != "rules" || len(event.Context) == 0 {
			continue
		}
		if requestID != "" && event.RequestID != requestID {
			continue
		}
		records = append(records, replayRecord{
			Key:       replayKey(event, len(records)),
			RequestID: event.RequestID,
			BundleID:  event.BundleID,
			Context:   cloneAnyMap(event.Context),
			Recorded:  cloneRuleMatches(event.Rules),
		})
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return records, nil
}

func replayKey(event audit.DecisionEvent, index int) string {
	switch {
	case strings.TrimSpace(event.RequestID) != "":
		return event.RequestID
	case strings.TrimSpace(event.BundleID) != "":
		return fmt.Sprintf("%s[%d]", event.BundleID, index)
	default:
		return fmt.Sprintf("audit[%d]", index)
	}
}

func printDecisionReport(report any, jsonOut bool) error {
	if jsonOut {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	switch value := report.(type) {
	case diffReport:
		fmt.Print(renderDiffReport(value))
		return nil
	case replayReport:
		fmt.Print(renderReplayReport(value))
		return nil
	default:
		return fmt.Errorf("unsupported decision report %T", report)
	}
}

func renderDiffReport(report diffReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "base: %s\n", report.BasePath)
	fmt.Fprintf(&b, "candidate: %s\n", report.CandidatePath)
	fmt.Fprintf(&b, "compared %d contexts\n", report.Compared)
	fmt.Fprintf(&b, "changed: %d\n", report.Changed)
	fmt.Fprintf(&b, "unchanged: %d\n", report.Unchanged)
	for _, diff := range report.Differences {
		renderDecisionDiff(&b, diff)
	}
	return b.String()
}

func renderReplayReport(report replayReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rules: %s\n", report.RulesPath)
	fmt.Fprintf(&b, "audit: %s\n", report.AuditPath)
	fmt.Fprintf(&b, "replayed %d audit decisions\n", report.Replayed)
	fmt.Fprintf(&b, "changed: %d\n", report.Changed)
	fmt.Fprintf(&b, "unchanged: %d\n", report.Unchanged)
	for _, diff := range report.Differences {
		renderDecisionDiff(&b, diff)
	}
	return b.String()
}

func renderDecisionDiff(b *strings.Builder, diff decisionDiff) {
	fmt.Fprintf(b, "\n[%s]\n", diff.Key)
	for _, match := range sortedRuleMatches(diff.Added) {
		fmt.Fprintf(b, "  added: %s\n", formatRuleMatch(match))
	}
	for _, match := range sortedRuleMatches(diff.Removed) {
		fmt.Fprintf(b, "  removed: %s\n", formatRuleMatch(match))
	}
	sort.Slice(diff.Changed, func(i, j int) bool { return diff.Changed[i].Name < diff.Changed[j].Name })
	for _, changed := range diff.Changed {
		fmt.Fprintf(b, "  changed: %s\n", changed.Name)
		if changed.Before != nil {
			fmt.Fprintf(b, "    before: %s\n", formatRuleMatch(*changed.Before))
		}
		if changed.After != nil {
			fmt.Fprintf(b, "    after:  %s\n", formatRuleMatch(*changed.After))
		}
	}
}

func formatRuleMatch(match audit.RuleMatch) string {
	label := match.Name + " -> " + match.Action
	if match.Fallback {
		label += " [fallback]"
	}
	if len(match.Params) == 0 {
		return label
	}
	return label + " " + stableJSON(match.Params)
}

func sortedRuleMatches(matches []audit.RuleMatch) []audit.RuleMatch {
	out := cloneRuleMatches(matches)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func cloneRuleMatches(matches []audit.RuleMatch) []audit.RuleMatch {
	out := make([]audit.RuleMatch, len(matches))
	for i, match := range matches {
		out[i] = normalizeRuleMatch(match)
	}
	return out
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneParams(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func normalizeRuleMatch(match audit.RuleMatch) audit.RuleMatch {
	return audit.RuleMatch{
		Name:     match.Name,
		Priority: match.Priority,
		Action:   match.Action,
		Params:   cloneParams(match.Params),
		Fallback: match.Fallback,
	}
}

func stableJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(raw)
}
