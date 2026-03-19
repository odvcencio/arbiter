// Package main implements the arbiter CLI tool.
//
// Commands:
//
//	arbiter emit <file.arb>                   — print Arishem JSON to stdout (default)
//	arbiter emit <file.arb> --format rego     — emit Rego (OPA) policy
//	arbiter emit <file.arb> --format cel      — emit CEL expressions
//	arbiter emit <file.arb> --format drools   — emit DRL (Drools Rule Language)
//	arbiter emit <file.arb> --rule Name       — emit a single rule's condition JSON
//	arbiter check <file.arb>                  — validate without emitting
//	arbiter compile <file.arb>                — compile to bytecode, print stats
//	arbiter eval <file.arb> --data '{...}'    — compile and eval against JSON
//	arbiter expert <file.arb> --envelope '{...}' [--facts '[...]'] — run one expert session
//	arbiter import <file.json> [-o output.arb] — decompile Arishem JSON to .arb
//	arbiter serve [--grpc :8081] [--audit-file decisions.jsonl] — start gRPC API
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/odvcencio/arbiter"
	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/decompile"
	"github.com/odvcencio/arbiter/emit"
	"github.com/odvcencio/arbiter/expert"
	"github.com/odvcencio/arbiter/grpcserver"
	"github.com/odvcencio/arbiter/overrides"
	"google.golang.org/grpc"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: arbiter <command> <file>\nCommands: emit, check, compile, eval, expert, import, serve\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "emit":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: arbiter emit <file.arb> [--format rego|cel|drools] [--rule Name]\n")
			os.Exit(1)
		}
		ruleName := ""
		format := ""
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--rule" && i+1 < len(os.Args) {
				ruleName = os.Args[i+1]
				i++
			}
			if os.Args[i] == "--format" && i+1 < len(os.Args) {
				format = os.Args[i+1]
				i++
			}
		}
		if err := emitCmd(os.Args[2], ruleName, format); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "check":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: arbiter check <file.arb>\n")
			os.Exit(1)
		}
		if err := check(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "compile":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: arbiter compile <file.arb>\n")
			os.Exit(1)
		}
		if err := compileCmd(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "eval":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: arbiter eval <file.arb> --data '{...}'\n")
			os.Exit(1)
		}
		dataJSON := ""
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--data" && i+1 < len(os.Args) {
				dataJSON = os.Args[i+1]
				i++
			}
		}
		if dataJSON == "" {
			fmt.Fprintf(os.Stderr, "error: --data flag is required\n")
			os.Exit(1)
		}
		if err := evalCmd(os.Args[2], dataJSON); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "expert":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: arbiter expert <file.arb> --envelope '{...}' [--facts '[...]']\n")
			os.Exit(1)
		}
		envelopeJSON := ""
		factsJSON := ""
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--envelope" && i+1 < len(os.Args) {
				envelopeJSON = os.Args[i+1]
				i++
			}
			if os.Args[i] == "--facts" && i+1 < len(os.Args) {
				factsJSON = os.Args[i+1]
				i++
			}
		}
		if envelopeJSON == "" {
			fmt.Fprintf(os.Stderr, "error: --envelope flag is required\n")
			os.Exit(1)
		}
		if err := expertCmd(os.Args[2], envelopeJSON, factsJSON); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "import":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: arbiter import <file.json> [-o output.arb]\n")
			os.Exit(1)
		}
		outPath := ""
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "-o" && i+1 < len(os.Args) {
				outPath = os.Args[i+1]
				i++
			}
		}
		if err := importCmd(os.Args[2], outPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		grpcAddr := ":8081"
		auditFile := ""
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--grpc" && i+1 < len(os.Args) {
				grpcAddr = os.Args[i+1]
				i++
			}
			if os.Args[i] == "--audit-file" && i+1 < len(os.Args) {
				auditFile = os.Args[i+1]
				i++
			}
		}
		if err := serveCmd(grpcAddr, auditFile); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nCommands: emit, check, compile, eval, expert, import, serve\n", os.Args[1])
		os.Exit(1)
	}
}

func emitCmd(path, ruleName, format string) error {
	source, err := arbiter.LoadFileSource(path)
	if err != nil {
		return err
	}

	// If a specific format is requested, use the emit package
	switch format {
	case "rego":
		out, err := emit.ToRego(source)
		if err != nil {
			return fmt.Errorf("emit rego: %w", err)
		}
		fmt.Print(out)
		return nil
	case "cel":
		out, err := emit.ToCEL(source)
		if err != nil {
			return fmt.Errorf("emit cel: %w", err)
		}
		fmt.Print(out)
		return nil
	case "drools":
		out, err := emit.ToDRL(source)
		if err != nil {
			return fmt.Errorf("emit drools: %w", err)
		}
		fmt.Print(out)
		return nil
	case "":
		// Default: Arishem JSON
	default:
		return fmt.Errorf("unknown format %q (supported: rego, cel, drools)", format)
	}

	if ruleName != "" {
		json, err := arbiter.TranspileRuleFile(path, ruleName)
		if err != nil {
			return fmt.Errorf("transpile rule %s: %w", ruleName, err)
		}
		fmt.Println(json)
		return nil
	}

	json, err := arbiter.TranspileFile(path)
	if err != nil {
		return fmt.Errorf("transpile %s: %w", path, err)
	}
	fmt.Println(json)
	return nil
}

func check(path string) error {
	_, err := arbiter.TranspileFile(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}

	fmt.Fprintf(os.Stderr, "%s: ok\n", path)
	return nil
}

func compileCmd(path string) error {
	rs, err := arbiter.CompileFile(path)
	if err != nil {
		return fmt.Errorf("compile %s: %w", path, err)
	}

	fmt.Printf("compiled %s\n", path)
	fmt.Printf("  rules:        %d\n", len(rs.Rules))
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

func serveCmd(grpcAddr, auditFile string) error {
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

	grpcSrv := grpc.NewServer()
	arbiterv1.RegisterArbiterServiceServer(grpcSrv, grpcserver.NewServer(grpcserver.NewRegistry(), overrides.NewStore(), sink))

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
