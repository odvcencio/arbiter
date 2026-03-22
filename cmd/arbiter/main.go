// Package main implements the arbiter CLI tool.
//
// Commands:
//
//	arbiter check <file.arb>                  — validate without emitting
//	arbiter compile <file.arb>                — compile to bytecode, print stats
//	arbiter eval <file.arb> --data '{...}'    — compile and eval against JSON
//	arbiter strategy <file.arb> [--name Name] --data '{...}' — evaluate one strategy
//	arbiter diff <base.arb> <candidate.arb> [--data '{...}' | --data-file contexts.json] [--key path] [--json] — compare governed outcomes
//	arbiter replay <rules.arb> --audit decisions.jsonl [--request-id id] [--limit N] [--json] — replay audited rule decisions
//	arbiter expert <file.arb> --envelope '{...}' [--facts '[...]'] — run one expert session
//	arbiter test [file.test.arb] [--verbose] — run executable bundle specs
//	arbiter import <file.json> [-o output.arb] — decompile Arishem JSON to .arb
//	arbiter serve [--grpc :8081] [--audit-file decisions.jsonl] [--bundle-file bundles.json] [--overrides-file overrides.json] — start gRPC API
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/odvcencio/arbiter"
	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/arbtest"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/decompile"
	"github.com/odvcencio/arbiter/expert"
	explorepkg "github.com/odvcencio/arbiter/explore"
	"github.com/odvcencio/arbiter/flags"
	"github.com/odvcencio/arbiter/grpcserver"
	"github.com/odvcencio/arbiter/overrides"
	"google.golang.org/grpc"
)

const (
	commandList = "check, compile, eval, strategy, diff, replay, expert, test, explore, import, serve"
	rootUsage   = "Usage: arbiter <command> <file>\nCommands: " + commandList
)

type usageError string

func (e usageError) Error() string { return string(e) }

var commandHandlers = map[string]func([]string) error{
	"check":    runCheck,
	"compile":  runCompile,
	"eval":     runEval,
	"strategy": runStrategy,
	"diff":     runDiff,
	"replay":   runReplay,
	"expert":   runExpert,
	"test":     runTest,
	"explore":  runExplore,
	"import":   runImport,
	"serve":    runServe,
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var usage usageError
		if errors.As(err, &usage) {
			fmt.Fprintln(os.Stderr, usage.Error())
		} else {
			fmt.Fprintln(os.Stderr, formatCLIError(err))
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError(rootUsage)
	}
	handler, ok := commandHandlers[args[0]]
	if !ok {
		return usageError(fmt.Sprintf("Unknown command: %s\nCommands: %s", args[0], commandList))
	}
	return handler(args[1:])
}

func runCheck(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter check <file.arb>")
	}
	return check(args[0])
}

func runCompile(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter compile <file.arb>")
	}
	return compileCmd(args[0])
}

func runEval(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter eval <file.arb> --data '{...}'")
	}
	dataJSON := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--data" && i+1 < len(args) {
			dataJSON = args[i+1]
			i++
		}
	}
	if dataJSON == "" {
		return fmt.Errorf("--data flag is required")
	}
	return evalCmd(args[0], dataJSON)
}

func runStrategy(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter strategy <file.arb> [--name Name] --data '{...}'")
	}
	name := ""
	dataJSON := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case "--data":
			if i+1 < len(args) {
				dataJSON = args[i+1]
				i++
			}
		}
	}
	if dataJSON == "" {
		return fmt.Errorf("--data flag is required")
	}
	return strategyCmd(args[0], name, dataJSON)
}

func runDiff(args []string) error {
	if len(args) < 2 {
		return usageError("Usage: arbiter diff <base.arb> <candidate.arb> [--data '{...}' | --data-file contexts.json] [--key path] [--json]")
	}
	dataJSON := ""
	dataFile := ""
	keyPath := ""
	jsonOut := false
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--data":
			if i+1 < len(args) {
				dataJSON = args[i+1]
				i++
			}
		case "--data-file":
			if i+1 < len(args) {
				dataFile = args[i+1]
				i++
			}
		case "--key":
			if i+1 < len(args) {
				keyPath = args[i+1]
				i++
			}
		case "--json":
			jsonOut = true
		}
	}
	return diffCmd(args[0], args[1], dataJSON, dataFile, keyPath, jsonOut)
}

func runReplay(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter replay <rules.arb> --audit decisions.jsonl [--request-id id] [--limit N] [--json]")
	}
	auditPath := ""
	requestID := ""
	limit := 0
	jsonOut := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--audit":
			if i+1 < len(args) {
				auditPath = args[i+1]
				i++
			}
		case "--request-id":
			if i+1 < len(args) {
				requestID = args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(args) {
				value, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("invalid --limit %q: %w", args[i+1], err)
				}
				limit = value
				i++
			}
		case "--json":
			jsonOut = true
		}
	}
	return replayCmd(args[0], auditPath, requestID, limit, jsonOut)
}

func runExpert(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter expert <file.arb> --envelope '{...}' [--facts '[...]']")
	}
	envelopeJSON := ""
	factsJSON := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--envelope" && i+1 < len(args) {
			envelopeJSON = args[i+1]
			i++
		}
		if args[i] == "--facts" && i+1 < len(args) {
			factsJSON = args[i+1]
			i++
		}
	}
	if envelopeJSON == "" {
		return fmt.Errorf("--envelope flag is required")
	}
	return expertCmd(args[0], envelopeJSON, factsJSON)
}

func runTest(args []string) error {
	path := ""
	verbose := false
	for _, arg := range args {
		switch arg {
		case "--verbose":
			verbose = true
		default:
			if path == "" {
				path = arg
				continue
			}
			return usageError("Usage: arbiter test [file.test.arb] [--verbose]")
		}
	}
	return testCmd(path, verbose)
}

func runImport(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter import <file.json> [-o output.arb]")
	}
	outPath := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) {
			outPath = args[i+1]
			i++
		}
	}
	return importCmd(args[0], outPath)
}

func runExplore(args []string) error {
	path := ""
	if len(args) > 0 {
		path = args[0]
	}
	return exploreCmd(path)
}

func runServe(args []string) error {
	grpcAddr := ":8081"
	auditFile := ""
	bundleFile := ""
	overridesFile := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--grpc" && i+1 < len(args) {
			grpcAddr = args[i+1]
			i++
		}
		if args[i] == "--audit-file" && i+1 < len(args) {
			auditFile = args[i+1]
			i++
		}
		if args[i] == "--bundle-file" && i+1 < len(args) {
			bundleFile = args[i+1]
			i++
		}
		if args[i] == "--overrides-file" && i+1 < len(args) {
			overridesFile = args[i+1]
			i++
		}
	}
	return serveCmd(grpcAddr, auditFile, bundleFile, overridesFile)
}

func formatCLIError(err error) string {
	if err == nil {
		return ""
	}
	if msg, ok := diagnosticString(err); ok {
		return msg
	}
	return fmt.Sprintf("error: %v", err)
}

func diagnosticString(err error) (string, bool) {
	var diag *arbiter.DiagnosticError
	if errors.As(err, &diag) {
		return diag.Error(), true
	}
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if looksLikeDiagnostic(cur.Error()) {
			return cur.Error(), true
		}
	}
	return "", false
}

func looksLikeDiagnostic(message string) bool {
	_, rest, ok := strings.Cut(message, ":")
	if !ok {
		return false
	}
	first, rest, ok := strings.Cut(rest, ":")
	if !ok {
		return false
	}
	if _, err := strconv.Atoi(strings.TrimSpace(first)); err != nil {
		return false
	}
	if second, _, ok := strings.Cut(rest, ":"); ok {
		if _, err := strconv.Atoi(strings.TrimSpace(second)); err == nil {
			return true
		}
	}
	return true
}

func check(path string) error {
	unit, parsed, err := arbiter.LoadFileParsed(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	full, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, arbiter.WrapFileError(unit, err))
	}
	if _, err := flags.LoadParsed(parsed, full); err != nil {
		return fmt.Errorf("check %s: %w", path, arbiter.WrapFileError(unit, err))
	}
	if _, err := expert.CompileParsed(parsed, full); err != nil {
		return fmt.Errorf("check %s: %w", path, arbiter.WrapFileError(unit, err))
	}
	if full.Strategies.Count() == 0 {
		if _, err := arbiter.TranspileParsed(parsed); err != nil {
			return fmt.Errorf("check %s: %w", path, arbiter.WrapFileError(unit, err))
		}
	}

	fmt.Fprintf(os.Stderr, "%s: ok\n", path)
	return nil
}

func compileCmd(path string) error {
	full, err := arbiter.CompileFullFile(path)
	if err != nil {
		return fmt.Errorf("compile %s: %w", path, err)
	}
	rs := full.Ruleset

	fmt.Printf("compiled %s\n", path)
	fmt.Printf("  rules:        %d\n", len(rs.Rules))
	fmt.Printf("  strategies:   %d\n", full.Strategies.Count())
	fmt.Printf("  actions:      %d\n", len(rs.Actions))
	fmt.Printf("  instructions: %d bytes\n", len(rs.Instructions))
	fmt.Printf("  strings:      %d\n", rs.Constants.StringCount())
	fmt.Printf("  numbers:      %d\n", rs.Constants.NumberCount())
	return nil
}

func evalCmd(path, dataJSON string) error {
	rs, err := arbiter.CompileFile(path)
	if err != nil {
		return fmt.Errorf("compile %s: %w", path, err)
	}

	dc, err := arbiter.DataFromJSON(dataJSON, rs)
	if err != nil {
		return fmt.Errorf("parse data: %w", err)
	}

	matched, err := arbiter.Eval(rs, dc)
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}

	if len(matched) == 0 {
		fmt.Println("no rules matched")
		return nil
	}

	for _, m := range matched {
		tag := "matched"
		if m.Fallback {
			tag = "fallback"
		}
		fmt.Printf("[%s] %s -> %s", tag, m.Name, m.Action)
		if len(m.Params) > 0 {
			out, _ := json.Marshal(m.Params)
			fmt.Printf(" %s", out)
		}
		fmt.Println()
	}

	return nil
}

func strategyCmd(path, name, dataJSON string) error {
	full, err := arbiter.CompileFullFile(path)
	if err != nil {
		return fmt.Errorf("load strategies: %w", err)
	}
	strategies := full.Strategies
	if name == "" {
		names := strategies.Names()
		switch len(names) {
		case 0:
			return fmt.Errorf("bundle contains no strategies")
		case 1:
			name = names[0]
		default:
			return fmt.Errorf("bundle contains multiple strategies; use --name (%s)", strings.Join(names, ", "))
		}
	}

	var ctx map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &ctx); err != nil {
		return fmt.Errorf("parse data: %w", err)
	}

	result, err := arbiter.EvalStrategy(full, name, ctx)
	if err != nil {
		return fmt.Errorf("strategy %s: %w", name, err)
	}
	blob, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal strategy result: %w", err)
	}
	fmt.Println(string(blob))
	return nil
}

func expertCmd(path, envelopeJSON, factsJSON string) error {
	program, err := expert.CompileFile(path)
	if err != nil {
		return fmt.Errorf("compile expert rules: %w", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal([]byte(envelopeJSON), &envelope); err != nil {
		return fmt.Errorf("parse envelope: %w", err)
	}

	facts, err := parseFactsJSON(factsJSON)
	if err != nil {
		return err
	}

	result, err := expert.NewSession(program, envelope, facts, expert.Options{}).Run(context.Background())
	if err != nil {
		return fmt.Errorf("run expert session: %w", err)
	}

	out := map[string]any{
		"outcomes":    result.Outcomes,
		"facts":       result.Facts,
		"activations": result.Activations,
		"rounds":      result.Rounds,
		"mutations":   result.Mutations,
		"stop_reason": result.StopReason,
	}
	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session result: %w", err)
	}
	fmt.Println(string(blob))
	return nil
}

func testCmd(path string, verbose bool) error {
	paths := []string(nil)
	if path == "" {
		files, err := filepath.Glob("*.test.arb")
		if err != nil {
			return fmt.Errorf("resolve tests: %w", err)
		}
		if len(files) == 0 {
			return fmt.Errorf("test requires a .test.arb path when the current directory has no *.test.arb files")
		}
		paths = files
	} else {
		paths = append(paths, path)
	}

	totalPassed := 0
	totalFailed := 0
	for _, item := range paths {
		result, err := arbtest.RunFile(item, arbtest.Options{Verbose: verbose})
		if err != nil {
			return fmt.Errorf("test %s: %w", item, err)
		}
		printTestResult(result)
		totalPassed += result.Passed
		totalFailed += result.Failed
	}

	fmt.Printf("test summary: %d passed, %d failed\n", totalPassed, totalFailed)
	if totalFailed > 0 {
		return fmt.Errorf("%d test cases failed", totalFailed)
	}
	return nil
}

func printTestResult(result *arbtest.FileResult) {
	if result == nil {
		return
	}
	fmt.Printf("%s: %d passed, %d failed\n", result.File, result.Passed, result.Failed)
	for _, item := range result.Cases {
		if !result.Verbose && item.Passed {
			continue
		}
		status := "PASS"
		if !item.Passed {
			status = "FAIL"
		}
		fmt.Printf("  [%s] %s %q\n", status, item.Kind, item.Name)
		for _, detail := range item.Details {
			fmt.Printf("    %s\n", detail)
		}
		if item.Error != "" {
			fmt.Printf("    error: %s\n", item.Error)
		}
	}
}

func exploreCmd(path string) error {
	if path == "" {
		files, err := filepath.Glob("*.arb")
		if err != nil {
			return fmt.Errorf("resolve bundle: %w", err)
		}
		switch len(files) {
		case 0:
			return fmt.Errorf("explore requires a bundle path when the current directory has no .arb files")
		case 1:
			path = files[0]
		default:
			return fmt.Errorf("explore requires a bundle path when the current directory has multiple .arb files")
		}
	}

	summary, err := explorepkg.BuildSummaryFile(path)
	if err != nil {
		return fmt.Errorf("explore %s: %w", path, err)
	}
	blob, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal explore summary: %w", err)
	}
	fmt.Println(string(blob))
	return nil
}

func parseFactsJSON(factsJSON string) ([]expert.Fact, error) {
	if factsJSON == "" {
		return nil, nil
	}
	var facts []expert.Fact
	if err := json.Unmarshal([]byte(factsJSON), &facts); err != nil {
		return nil, fmt.Errorf("parse facts: %w", err)
	}
	return facts, nil
}

// importRuleJSON is the expected JSON structure for each rule in the input file.
type importRuleJSON struct {
	Name      string `json:"name"`
	Priority  int    `json:"priority"`
	Condition any    `json:"condition"`
	Action    any    `json:"action,omitempty"`
	Fallback  any    `json:"fallback,omitempty"`
}

func importCmd(path, outPath string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Try parsing as the TranspileResult format (with "rules" key)
	var wrapper struct {
		Rules []importRuleJSON `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && len(wrapper.Rules) > 0 {
		return importRules(wrapper.Rules, outPath)
	}

	// Try parsing as a bare array of rules
	var ruleArr []importRuleJSON
	if err := json.Unmarshal(data, &ruleArr); err == nil && len(ruleArr) > 0 {
		return importRules(ruleArr, outPath)
	}

	// Try parsing as a single rule
	var single importRuleJSON
	if err := json.Unmarshal(data, &single); err == nil && single.Name != "" {
		return importRules([]importRuleJSON{single}, outPath)
	}

	return fmt.Errorf("cannot parse %s: expected Arishem JSON with rules array, rule array, or single rule", path)
}

func serveCmd(grpcAddr, auditFile, bundleFile, overridesFile string) error {
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", grpcAddr, err)
	}

	var sink audit.Sink = audit.NopSink{}
	var closer interface{ Close() error }
	if auditFile != "" {
		fileSink, err := audit.NewJSONLSink(auditFile)
		if err != nil {
			return fmt.Errorf("open audit sink: %w", err)
		}
		sink = fileSink
		closer = fileSink
	}
	if closer != nil {
		defer closer.Close()
	}

	registry := grpcserver.NewRegistry()
	if bundleFile != "" {
		registry, err = grpcserver.NewFileRegistry(bundleFile)
		if err != nil {
			return fmt.Errorf("open bundle registry: %w", err)
		}
	}
	store := overrides.NewStore()
	if overridesFile != "" {
		store, err = overrides.NewFileStore(overridesFile)
		if err != nil {
			return fmt.Errorf("open override store: %w", err)
		}
	}

	grpcSrv := grpc.NewServer()
	arbiterv1.RegisterArbiterServiceServer(grpcSrv, grpcserver.NewServer(registry, store, sink))

	fmt.Fprintf(os.Stderr, "arbiter gRPC listening on %s\n", grpcAddr)
	return grpcSrv.Serve(lis)
}

func importRules(rules []importRuleJSON, outPath string) error {
	var arishemRules []decompile.ArishemRule

	for _, r := range rules {
		ar := decompile.ArishemRule{
			Name:     r.Name,
			Priority: r.Priority,
		}

		if r.Condition != nil {
			b, err := json.Marshal(r.Condition)
			if err != nil {
				return fmt.Errorf("rule %s: marshal condition: %w", r.Name, err)
			}
			ar.Condition = string(b)
		}
		if r.Action != nil {
			b, err := json.Marshal(r.Action)
			if err != nil {
				return fmt.Errorf("rule %s: marshal action: %w", r.Name, err)
			}
			ar.Action = string(b)
		}
		if r.Fallback != nil {
			b, err := json.Marshal(r.Fallback)
			if err != nil {
				return fmt.Errorf("rule %s: marshal fallback: %w", r.Name, err)
			}
			ar.Fallback = string(b)
		}

		arishemRules = append(arishemRules, ar)
	}

	arb, err := decompile.ArishemToArb(arishemRules)
	if err != nil {
		return fmt.Errorf("decompile: %w", err)
	}

	if outPath != "" {
		if err := os.WriteFile(outPath, []byte(arb), 0644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d rules)\n", outPath, len(rules))
		return nil
	}

	fmt.Print(arb)
	return nil
}
