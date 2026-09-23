package arbiter

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// TestGoldenParseResults pins the parser's exact output (error/no-error, and
// the full S-expression) for a representative slice of the grammar: rules,
// expert rules, flags, strategies, segments, fact/outcome, input, const,
// tables, quantifiers, joins, aggregates, lookups, arbiter declarations,
// templates, imports, and workers.
//
// gotreesitter is an external dependency with 14 downstream importers, and
// the embedded grammar.bin parse table is tied to the exact gotreesitter
// version it was generated with (see TestGrammarBinIsCurrent in
// grammar/grammar_blob_test.go). A gotreesitter upgrade can change parser
// behavior — stricter error detection, different GLR conflict resolution,
// renamed or restructured grammar nodes — without touching this repo's own
// grammar definition. Structural substring checks (see TestParse* above) can
// miss that drift; an exact golden comparison cannot.
//
// If this test fails after a gotreesitter bump: read the diff, decide
// whether the new behavior is correct, and update the golden value (do not
// just silence the failure).
func TestGoldenParseResults(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		wantError bool
		wantSExpr string
	}{
		{
			name:      "rule_basic",
			src:       `rule T { when { x > 1 } then A {} }`,
			wantSExpr: `(source_file (rule_declaration (identifier) (when_block (comparison_expr (identifier) (number_literal))) (then_block (identifier))))`,
		},
		{
			name:      "rule_full",
			src:       `rule T priority 5 { when { x > 1 and y < 2 } then A { k: "v" } otherwise B { k: "d" } }`,
			wantSExpr: `(source_file (rule_declaration (identifier) (rule_priority (number_literal)) (when_block (and_expr (comparison_expr (identifier) (number_literal)) (comparison_expr (identifier) (number_literal)))) (then_block (identifier) (param_assignment (identifier) (string_literal))) (otherwise_block (identifier) (param_assignment (identifier) (string_literal)))))`,
		},
		{
			name:      "expert_rule",
			src:       `expert rule T { when { x > 0 } then assert F { key: "k" } }`,
			wantSExpr: `(source_file (expert_rule_declaration (identifier) (expert_when_block (comparison_expr (identifier) (number_literal))) (expert_then_block (identifier) (param_assignment (identifier) (string_literal)))))`,
		},
		{
			name:      "fact_outcome",
			src:       "fact F {\n\tk: string\n\tv?: number\n}\noutcome O {\n\ts: string\n}",
			wantSExpr: `(source_file (fact_declaration (identifier) (schema_field_declaration (identifier) (schema_type_name)) (schema_field_declaration (identifier) (schema_type_name))) (outcome_declaration (identifier) (schema_field_declaration (identifier) (schema_type_name))))`,
		},
		{
			name:      "input_block",
			src:       `input { user: { id: string age: number } }`,
			wantSExpr: `(source_file (input_declaration (input_field (identifier) (input_field (identifier) (input_field_type)) (input_field (identifier) (input_field_type)))))`,
		},
		{
			name:      "const_scalar",
			src:       `const LIMIT = 100`,
			wantSExpr: `(source_file (const_declaration (identifier) (number_literal)))`,
		},
		{
			name:      "const_list",
			src:       `const TIERS = ["gold", "platinum"]`,
			wantSExpr: `(source_file (const_declaration (identifier) (list_literal (string_literal) (string_literal))))`,
		},
		{
			name:      "segment",
			src:       `segment high { x > 1 }`,
			wantSExpr: `(source_file (segment_declaration (identifier) (comparison_expr (identifier) (number_literal))))`,
		},
		{
			name:      "flag",
			src:       "flag f type boolean default false {\n\twhen { true } then true\n}",
			wantSExpr: `(source_file (flag_declaration (identifier) (bool_literal) (flag_rule (bool_literal) (bool_literal))))`,
		},
		{
			name:      "strategy",
			src:       "outcome O { t: string }\nstrategy S returns O {\n\twhen { x > 1 } then A { t: \"a\" }\n\telse B { t: \"b\" }\n}",
			wantSExpr: `(source_file (outcome_declaration (identifier) (schema_field_declaration (identifier) (schema_type_name))) (strategy_declaration (identifier) (identifier) (strategy_candidate (strategy_when_candidate (when_block (comparison_expr (identifier) (number_literal))) (identifier) (param_assignment (identifier) (string_literal)))) (strategy_candidate (strategy_else_candidate (identifier) (param_assignment (identifier) (string_literal))))))`,
		},
		{
			name:      "table",
			src:       "table t {\n    x: number | y: string\n    1 | \"a\"\n}",
			wantSExpr: `(source_file (table_declaration (identifier) (table_header_row (table_column_header (identifier) (schema_type_name)) (table_column_header (identifier) (schema_type_name))) (table_data_row (number_literal) (string_literal))))`,
		},
		{
			name:      "quantifier",
			src:       `rule T { when { any i in xs { i > 0 } } then A {} }`,
			wantSExpr: `(source_file (rule_declaration (identifier) (when_block (quantifier_expr (identifier) (identifier) (comparison_expr (identifier) (number_literal)))) (then_block (identifier))))`,
		},
		{
			name:      "join",
			src:       `expert rule T { when { join a: S, b: S on .zone { a.v - b.v > 1 } } then emit A {} }`,
			wantSExpr: `(source_file (expert_rule_declaration (identifier) (expert_when_block (join_expr (join_expr_shorthand (join_binding (identifier) (identifier)) (join_binding (identifier) (identifier)) (identifier) (comparison_expr (math_expr (member_expr (identifier) (identifier)) (member_expr (identifier) (identifier))) (number_literal))))) (expert_then_block (identifier))))`,
		},
		{
			name:      "aggregate",
			src:       `rule T { when { sum(i.p for i in cart.items) > 10 } then A {} }`,
			wantSExpr: `(source_file (rule_declaration (identifier) (when_block (comparison_expr (aggregate_expr (identifier) (member_expr (identifier) (identifier)) (identifier) (member_expr (identifier) (identifier))) (number_literal))) (then_block (identifier))))`,
		},
		{
			name:      "lookup",
			src:       "rule R { when { true } then A { let row = lookup t where x > 0 order by x desc else { x: 0 }\nv: row.x } }",
			wantSExpr: `(source_file (rule_declaration (identifier) (when_block (bool_literal)) (then_block (identifier) (let_binding (identifier) (lookup_expr (identifier) (lookup_where_clause (comparison_expr (identifier) (number_literal))) (lookup_order_clause (identifier)) (lookup_else_clause (param_assignment (identifier) (number_literal))))) (param_assignment (identifier) (member_expr (identifier) (identifier))))))`,
		},
		{
			name:      "arbiter_decl",
			src:       "arbiter a {\n\tpoll 5s\n\ton * stdout\n}",
			wantSExpr: `(source_file (arbiter_declaration (identifier) (arbiter_poll_clause (duration_literal)) (arbiter_handler_clause (arbiter_wildcard) (arbiter_handler_kind))))`,
		},
		{
			name:      "template",
			src:       "template AtLeast(v, f) = v >= f",
			wantSExpr: `(source_file (template_declaration (identifier) (identifier) (identifier) (comparison_expr (identifier) (identifier))))`,
		},
		{
			name:      "import",
			src:       `import "fraud/scoring" as fs`,
			wantSExpr: `(source_file (import_declaration (string_literal) (identifier)))`,
		},
		{
			name:      "worker",
			src:       `worker W { input I output O exec "cmd" }`,
			wantSExpr: `(source_file (worker_declaration (identifier) (worker_input_clause (identifier)) (worker_output_clause (identifier)) (worker_runtime_clause (worker_handler_kind) (arbiter_target (string_literal)))))`,
		},
		{
			name:      "parse_error",
			src:       `rule T { when { x > } then A {} }`,
			wantError: true,
			wantSExpr: `(ERROR (identifier) (comparison_expr (identifier) (identifier)) (_expr (ERROR)))`,
		},
		{
			name:      "const_cycle",
			src:       "const C = C",
			wantSExpr: `(source_file (const_declaration (identifier) (identifier)))`,
			// The cycle itself is a semantic error caught by validateProgram,
			// not a syntax error — see TestConstSelfCycleRejected.
		},
		{
			// Canary for the grammar/test mismatch fixed alongside this test:
			// the current pinned gotreesitter (and this repo's grammar.bin,
			// which has no comma separator in input_field) silently drops the
			// unexpected comma instead of reporting it. A gotreesitter bump
			// that tightens GLR error recovery is expected to flip this
			// entry's wantError to true; if it does, that is exactly the
			// "fails loudly" signal this test exists for. It is not evidence
			// that input fields should take commas — see
			// TestTemplateReusedWithDifferentArgs and the real .arb files in
			// chitin-choir / buckley / continuum for the authoritative,
			// comma-free syntax.
			name:      "comma_input_leniency_canary",
			src:       `input { a: number, b: number }`,
			wantSExpr: `(source_file (input_declaration (input_field (identifier) (input_field_type)) (input_field (identifier) (input_field_type))))`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lang, err := GetLanguage()
			if err != nil {
				t.Fatalf("GetLanguage: %v", err)
			}
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			root := tree.RootNode()
			gotError := root.HasError()
			gotSExpr := root.SExpr(lang)

			if gotError != tc.wantError {
				t.Errorf("HasError: got %v, want %v\nSExpr: %s", gotError, tc.wantError, gotSExpr)
			}
			if gotSExpr != tc.wantSExpr {
				t.Errorf("SExpr mismatch:\n got:  %s\n want: %s", gotSExpr, tc.wantSExpr)
			}
		})
	}
}
