// Package main implements the arbiter CLI tool.
//
// Commands:
//
//	arbiter emit <file.arb>                — print Arishem JSON to stdout
//	arbiter emit <file.arb> --rule Name    — emit a single rule's condition JSON
//	arbiter check <file.arb>               — validate without emitting
//	arbiter compile <file.arb>             — compile to bytecode, print stats
//	arbiter eval <file.arb> --data '{...}' — compile and eval against JSON
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/odvcencio/arbiter"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: arbiter <command> <file.arb>\nCommands: emit, check, compile, eval\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "emit":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: arbiter emit <file.arb> [--rule Name]\n")
			os.Exit(1)
		}
		ruleName := ""
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--rule" && i+1 < len(os.Args) {
				ruleName = os.Args[i+1]
				i++
			}
		}
		if err := emit(os.Args[2], ruleName); err != nil {
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
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nCommands: emit, check, compile, eval\n", os.Args[1])
		os.Exit(1)
	}
}

func emit(path, ruleName string) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
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
