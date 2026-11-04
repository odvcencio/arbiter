package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	arbiter "m31labs.dev/arbiter"
)

func runRepl(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter repl <file.arb>\n\nCompiles the ruleset once, then evaluates a JSON context per line from stdin.")
	}
	prog, err := arbiter.CompileFile(args[0])
	if err != nil {
		return fmt.Errorf("repl %s: %w", args[0], err)
	}
	for _, w := range prog.Warnings {
		fmt.Fprintf(os.Stderr, "%s:%d:%d: warning: %s\n", args[0], w.Line, w.Col, w.Message)
	}
	fmt.Fprintf(os.Stderr, "loaded %s (%d rules) — type a JSON context per line, or 'exit'.\n", args[0], len(prog.Ruleset.Rules))
	return repl(prog, os.Stdin, os.Stdout)
}

// repl reads one JSON context per line from in, evaluates the compiled program,
// and writes the matched rules to out. It loops until EOF or "exit"/"quit".
func repl(prog *arbiter.Program, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for {
		fmt.Fprint(out, "arb> ")
		if !sc.Scan() {
			break
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		var ctx map[string]any
		if err := json.Unmarshal([]byte(line), &ctx); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			continue
		}
		matched, _, err := arbiter.EvalGoverned(prog, arbiter.DataFromMap(ctx, prog), prog.Segments, ctx)
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			continue
		}
		if len(matched) == 0 {
			fmt.Fprintln(out, "(no rules matched)")
			continue
		}
		for _, m := range matched {
			tag := "matched"
			if m.Fallback {
				tag = "fallback"
			}
			fmt.Fprintf(out, "[%s] %s -> %s", tag, m.Name, m.Action)
			if len(m.Params) > 0 {
				if b, err := json.Marshal(m.Params); err == nil {
					fmt.Fprintf(out, " %s", b)
				}
			}
			fmt.Fprintln(out)
		}
	}
	return sc.Err()
}
