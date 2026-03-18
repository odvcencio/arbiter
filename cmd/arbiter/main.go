// Package main implements the arbiter CLI tool.
//
// Commands:
//
//	arbiter emit <file.arb>                — print Arishem JSON to stdout
//	arbiter emit <file.arb> --rule Name    — emit a single rule's condition JSON
//	arbiter check <file.arb>               — validate without emitting
package main

import (
	"fmt"
	"os"

	"github.com/odvcencio/arbiter"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: arbiter <command> <file.arb>\nCommands: emit, check\n")
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
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nCommands: emit, check\n", os.Args[1])
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
