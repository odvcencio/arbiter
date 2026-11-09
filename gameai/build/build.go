// Package build compiles .arb decision logic into bundles for package gameai.
// It runs at build time (a build tool, `arbiter bundle --game`, or a server
// in development mode), so unlike gameai it links the .arb parser.
//
// A build step calls CompileFile and writes Result.Bundle next to the game's
// other assets; the game embeds or fetches that file and passes it to
// gameai.Load. The output is deterministic: the same source always gives the
// same bytes, so the bundle can be cached and its digest compared across
// machines.
package build

import (
	"fmt"
	"sort"
	"strings"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle/wire"
	"m31labs.dev/arbiter/gameai"
)

// Options controls bundle output.
type Options struct {
	// KeepNames keeps rule names readable, so decision traces and
	// Choice.Rule show them. Without it rule names are replaced by hashes;
	// action names, parameter keys and every string the rules compare or
	// return stay readable either way. Key per-agent memory by an action
	// parameter (such as a kind) rather than by rule name if you ship
	// hashed bundles.
	KeepNames bool
}

// Result is a compiled game bundle.
type Result struct {
	// Bundle is the bundle to ship; gameai.Load reads it.
	Bundle []byte
	// Digest is the hex SHA-256 of Bundle, equal to Program.DigestHex after
	// gameai.Load.
	Digest string
	// Rules is the number of rules in the bundle.
	Rules int
	// Tags lists the tags that select at least one rule, sorted. Each is a
	// decision a gameai.Spec can name.
	Tags []string
	// Program is the loaded bundle, ready for gameai.NewBrain.
	Program *gameai.Program
}

// Compile compiles one .arb source buffer. Imports and includes are not
// resolved; use CompileFile for multi-file programs.
func Compile(src []byte, opts Options) (*Result, error) {
	prog, err := arbiter.Compile(src)
	if err != nil {
		return nil, err
	}
	return finish(prog, opts)
}

// CompileFile compiles a .arb file, resolving its imports and includes.
func CompileFile(path string, opts Options) (*Result, error) {
	prog, err := arbiter.CompileFile(path)
	if err != nil {
		return nil, err
	}
	return finish(prog, opts)
}

func finish(prog *arbiter.Program, opts Options) (*Result, error) {
	if err := checkDeclarations(prog); err != nil {
		return nil, err
	}
	// FromRuleset applies the same checks gameai.Load does (no wall clock,
	// no governance), so a bundle that builds also loads.
	if _, err := gameai.FromRuleset(prog.Ruleset); err != nil {
		return nil, err
	}
	var data []byte
	var err error
	if opts.KeepNames {
		data, err = wire.Marshal(prog.Ruleset)
	} else {
		data, err = wire.MarshalObfuscated(prog.Ruleset, wire.ObfuscateOptions{HashRuleNames: true, HashSegmentNames: true})
	}
	if err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	loaded, err := gameai.Load(data)
	if err != nil {
		return nil, err
	}
	return &Result{
		Bundle:  data,
		Digest:  loaded.DigestHex(),
		Rules:   len(prog.Ruleset.Rules),
		Tags:    ruleTags(prog),
		Program: loaded,
	}, nil
}

// checkDeclarations rejects declarations a bundle does not carry. A bundle
// holds plain rules (and the tables and constants they use); strategies,
// flags, expert rules, arbiters and workers would be silently dropped.
func checkDeclarations(prog *arbiter.Program) error {
	ir := prog.IR
	if ir == nil {
		return nil
	}
	var dropped []string
	add := func(kind string, n int) {
		if n > 0 {
			dropped = append(dropped, fmt.Sprintf("%d %s", n, kind))
		}
	}
	add("strategy declarations", len(ir.Strategies))
	add("flag declarations", len(ir.Flags))
	add("expert rules", len(ir.Expert))
	add("arbiter declarations", len(ir.Arbiters))
	add("worker declarations", len(ir.Workers))
	if len(dropped) > 0 {
		return fmt.Errorf("game bundles carry plain rules only; the source has %s (write each decision as rules with a shared tag and pick a gameai.Selection)", strings.Join(dropped, ", "))
	}
	return nil
}

func ruleTags(prog *arbiter.Program) []string {
	seen := map[string]bool{}
	if prog.IR != nil {
		for _, rule := range prog.IR.Rules {
			for _, tag := range rule.Tags {
				seen[tag] = true
			}
		}
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}
