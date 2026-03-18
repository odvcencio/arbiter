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
//	arbiter import <file.json> [-o output.arb] — decompile Arishem JSON to .arb
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/decompile"
	"github.com/odvcencio/arbiter/emit"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: arbiter <command> <file>\nCommands: emit, check, compile, eval, import\n")
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
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nCommands: emit, check, compile, eval, import\n", os.Args[1])
		os.Exit(1)
	}
}

func emitCmd(path, ruleName, format string) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
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
		json, err := arbiter.TranspileRule(source, ruleName)
		if err != nil {
			return fmt.Errorf("transpile rule %s: %w", ruleName, err)
		}
		fmt.Println(json)
		return nil
	}

	json, err := arbiter.Transpile(source)
	if err != nil {
		return fmt.Errorf("transpile %s: %w", path, err)
	}
	fmt.Println(json)
	return nil
}

func check(path string) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	_, err = arbiter.Transpile(source)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}

	fmt.Fprintf(os.Stderr, "%s: ok\n", path)
	return nil
}

func compileCmd(path string) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	rs, err := arbiter.Compile(source)
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
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	rs, err := arbiter.Compile(source)
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
