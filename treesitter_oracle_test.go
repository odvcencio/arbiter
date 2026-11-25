package arbiter

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// oracleConditions mix the logical operators with the condition and
// arithmetic tiers below them. TestTreeSitterOracle parses each one inside a
// rule.
var oracleConditions = []string{
	`not x in [1, 2] and y`,
	`x not in [1] or not y is not null`,
	`a > 1 and not b == 2 or c between [1, 2]`,
	`not a contains "x" and b not contains "y"`,
	`not a retains b or a not retains c and d`,
	`a + b * c >= 3 and not d`,
	`x between [1, 2) or y in [1, 2] and not z between (0, 1]`,
	`any i in items { i > 1 and not i == 3 } or ok`,
	`not user.name starts_with "Dr" and user.email ends_with ".edu" or user.code matches "^[A-Z]"`,
	`a is null or not b is null and c is not null`,
	`a >= 1 and b <= 2 or c != 3 and not d > 4`,
	`not not a or not b and not not c`,
	`(a or b) and not (c and d) or e`,
}

// oracleSources exercise the grammar's declared conflicts: a schedule clause
// with and without a following source clause, aggregates next to calls, and
// the full and shorthand join forms.
var oracleSources = []string{
	`arbiter trading_system {
	stream wss://exchange.com/prices
	schedule "0 8 * * MON-FRI" source https://calendar.api/market-hours
	poll 30s
	source chain://risk_facts
	checkpoint /var/lib/arbiter/trading.state
	on Opportunity where confidence > 0.8 chain ai_analysis
	on * audit /var/log/trading.jsonl
}
arbiter nightly {
	schedule "0 2 * * *"
	source chain://batch
	on * audit /var/log/nightly.jsonl
}
`,
	`rule Totals {
	when { count(item in cart.items) > 1 and sum(item.price for item in cart.items) > 100 or max(a, b) > abs(c) }
	then Match { n: count(i in items), total: sum(i.n for i in items), top: max(x, y) }
}
`,
	`expert rule Shorthand {
	when { join a: Sensor, b: Sensor on .zone { abs(a.t - b.t) > 5 } }
	then emit Alert { zone: a.zone }
}
expert rule Full {
	when { join a: Sensor, b: Sensor, c: Sensor on a.zone == b.zone and not b.zone == c.zone { a.t > c.t or a.t < b.t } }
	then emit Alert { zone: a.zone }
}
`,
}

// TestTreeSitterOracle compares the embedded parse table with a parser that
// tree-sitter's own generator builds from grammar/grammar.json. It checks that
// the tree-sitter CLI accepts the grammar's declared conflicts and reports
// none of them as unnecessary, and that both parsers produce the same tree
// for every .arb file under examples/ and testdata/, for every condition in
// TestBooleanPrecedenceMatchesReference, and for oracleConditions and
// oracleSources.
//
// It is opt-in because it needs tools outside Go:
//
//	ARBITER_TREE_SITTER_ORACLE=1 go test -run TestTreeSitterOracle -count=1 .
//
// with the tree-sitter CLI (0.26 or later) and a C compiler on PATH.
func TestTreeSitterOracle(t *testing.T) {
	if os.Getenv("ARBITER_TREE_SITTER_ORACLE") != "1" {
		t.Skip("set ARBITER_TREE_SITTER_ORACLE=1 to compare trees with the tree-sitter CLI")
	}
	cli, err := exec.LookPath("tree-sitter")
	if err != nil {
		t.Fatalf("ARBITER_TREE_SITTER_ORACLE=1 but the tree-sitter CLI is not on PATH: %v", err)
	}
	grammarJSON, err := os.ReadFile(filepath.Join("grammar", "grammar.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(grammarJSON, &meta); err != nil || meta.Name == "" {
		t.Fatalf("read grammar name from grammar.json: %v", err)
	}

	dir := t.TempDir()
	writeFile := func(rel string, data []byte) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(args ...string) (string, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Dir = dir
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	writeFile("grammar.json", grammarJSON)
	stdout, stderr, err := run("generate", "--abi", "14", "grammar.json")
	if err != nil {
		t.Fatalf("tree-sitter generate rejected grammar.json: %v\n%s%s", err, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "unnecessary conflicts") {
		t.Errorf("tree-sitter generate reports declared conflicts as unnecessary:\n%s%s", stdout, stderr)
	}
	// tree-sitter build reads src/grammar.json for the language name.
	writeFile(filepath.Join("src", "grammar.json"), grammarJSON)
	lib := filepath.Join(dir, "oracle-parser.so")
	if stdout, stderr, err := run("build", "-o", lib, "."); err != nil {
		t.Fatalf("tree-sitter build: %v\n%s%s", err, stdout, stderr)
	}

	type input struct {
		name string
		src  []byte
	}
	var inputs []input
	for _, root := range []string{"examples", "testdata"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".arb") {
				return err
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			inputs = append(inputs, input{p, src})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].name < inputs[j].name })
	exprs := enumerateBoolExprs("abcd", 4)
	const chunk = 1000
	for start := 0; start < len(exprs); start += chunk {
		var b strings.Builder
		for i := start; i < min(start+chunk, len(exprs)); i++ {
			fmt.Fprintf(&b, "rule R%d { when { %s } then A {} }\n", i, exprs[i])
		}
		inputs = append(inputs, input{fmt.Sprintf("boolean expressions %d-%d", start, min(start+chunk, len(exprs))-1), []byte(b.String())})
	}
	var mixed strings.Builder
	for i, cond := range oracleConditions {
		fmt.Fprintf(&mixed, "rule M%d { when { %s } then A {} }\n", i, cond)
	}
	inputs = append(inputs, input{"oracleConditions", []byte(mixed.String())})
	for i, src := range oracleSources {
		inputs = append(inputs, input{fmt.Sprintf("oracleSources[%d]", i), []byte(src)})
	}

	lang, err := GetLanguage()
	if err != nil {
		t.Fatal(err)
	}
	same, bothRejected := 0, 0
	for i, in := range inputs {
		path := writeFile(filepath.Join("inputs", fmt.Sprintf("%03d.arb", i)), in.src)
		cliTree, stderr, runErr := run("parse", "-l", lib, "--lang-name", meta.Name, "--no-ranges", path)
		if strings.TrimSpace(cliTree) == "" {
			t.Errorf("%s: tree-sitter parse printed no tree: %v\n%s", in.name, runErr, stderr)
			continue
		}
		cliTree = collapseSpace(cliTree)
		cliHasError := strings.Contains(cliTree, "(ERROR") || strings.Contains(cliTree, "(MISSING")

		tree, err := gotreesitter.NewParser(lang).Parse(in.src)
		if err != nil {
			t.Errorf("%s: gotreesitter parse: %v", in.name, err)
			continue
		}
		root := tree.RootNode()
		switch {
		case root.HasError() && cliHasError:
			bothRejected++
			continue
		case root.HasError() != cliHasError:
			t.Errorf("%s: gotreesitter error=%v, tree-sitter error=%v\n  gotreesitter: %s\n  tree-sitter:  %s",
				in.name, root.HasError(), cliHasError, cliStyleTree(root, lang), cliTree)
			continue
		}
		if got := cliStyleTree(root, lang); got != cliTree {
			t.Errorf("%s: trees differ\n%s", in.name, firstDifference(got, cliTree))
			continue
		}
		same++
	}
	t.Logf("%d inputs: %d identical trees, %d rejected by both parsers", len(inputs), same, bothRejected)
}

var spaceRun = regexp.MustCompile(`\s+`)

func collapseSpace(s string) string { return strings.TrimSpace(spaceRun.ReplaceAllString(s, " ")) }

// cliStyleTree prints a tree the way `tree-sitter parse --no-ranges` does,
// with field names and named nodes only, on one line.
func cliStyleTree(n *gotreesitter.Node, lang *gotreesitter.Language) string {
	var b strings.Builder
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		b.WriteString("(" + n.Type(lang))
		for i := 0; i < n.ChildCount(); i++ {
			c := n.Child(i)
			if !c.IsNamed() {
				continue
			}
			b.WriteString(" ")
			if field := n.FieldNameForChild(i, lang); field != "" {
				b.WriteString(field + ": ")
			}
			walk(c)
		}
		b.WriteString(")")
	}
	walk(n)
	return b.String()
}

func firstDifference(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	from := max(0, i-160)
	return fmt.Sprintf("  gotreesitter: ...%s\n  tree-sitter:  ...%s", got[from:min(len(got), i+160)], want[from:min(len(want), i+160)])
}
