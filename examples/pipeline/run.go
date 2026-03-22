//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/odvcencio/arbiter/expert"
	"github.com/odvcencio/arbiter/expert/factsource"
)

func main() {
	leadsPath := "examples/pipeline/leads.csv"
	rulesPath := "examples/pipeline/sales.arb"
	outcomesPath := "examples/pipeline/outcomes.jsonl"

	if len(os.Args) > 1 {
		leadsPath = os.Args[1]
	}
	if len(os.Args) > 2 {
		rulesPath = os.Args[2]
	}
	if len(os.Args) > 3 {
		outcomesPath = os.Args[3]
	}

	// Load facts from CSV
	csvFacts, err := factsource.Load(leadsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load %s: %v\n", leadsPath, err)
		os.Exit(1)
	}

	// Convert to expert facts
	initialFacts := make([]expert.Fact, len(csvFacts))
	for i, f := range csvFacts {
		initialFacts[i] = expert.Fact{Type: f.Type, Key: f.Key, Fields: f.Fields}
	}

	// Compile arbys
	prog, err := expert.CompileFile(rulesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compile %s: %v\n", rulesPath, err)
		os.Exit(1)
	}

	// Run
	session := expert.NewSession(prog, map[string]any{}, initialFacts, expert.Options{MaxRounds: 32})
	result, err := session.Run(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("quiesced in %d rounds, %d mutations\n", result.Rounds, result.Mutations)
	fmt.Printf("qualified: %d leads\n", len(result.Facts))
	fmt.Printf("outcomes: %d\n\n", len(result.Outcomes))

	// Print outcomes to stdout
	for _, o := range result.Outcomes {
		fmt.Printf("[%s] %v\n", o.Name, o.Params)
	}

	// Save outcomes to JSONL
	outcomeFacts := make([]factsource.Fact, len(result.Outcomes))
	for i, o := range result.Outcomes {
		fields := make(map[string]any, len(o.Params)+2)
		fields["rule"] = o.Rule
		fields["name"] = o.Name
		for k, v := range o.Params {
			fields[k] = v
		}
		outcomeFacts[i] = factsource.Fact{
			Type:   o.Name,
			Key:    fmt.Sprintf("%s_%d", o.Name, i),
			Fields: fields,
		}
	}

	if err := factsource.Save(outcomesPath, outcomeFacts); err != nil {
		fmt.Fprintf(os.Stderr, "save %s: %v\n", outcomesPath, err)
		os.Exit(1)
	}
	fmt.Printf("\nwrote %d outcomes to %s\n", len(outcomeFacts), outcomesPath)
}
