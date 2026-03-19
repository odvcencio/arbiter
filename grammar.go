package arbiter

import (
	"github.com/odvcencio/gotreesitter/grammargen"
)

var (
	Str         = grammargen.Str
	Pat         = grammargen.Pat
	Sym         = grammargen.Sym
	Seq         = grammargen.Seq
	Choice      = grammargen.Choice
	Repeat      = grammargen.Repeat
	Repeat1     = grammargen.Repeat1
	Optional    = grammargen.Optional
	Token       = grammargen.Token
	ImmToken    = grammargen.ImmToken
	Field       = grammargen.Field
	Prec        = grammargen.Prec
	PrecLeft    = grammargen.PrecLeft
	PrecRight   = grammargen.PrecRight
	PrecDynamic = grammargen.PrecDynamic
	Alias       = grammargen.Alias
	Blank       = grammargen.Blank
	CommaSep    = grammargen.CommaSep
	CommaSep1   = grammargen.CommaSep1

	NewGrammar       = grammargen.NewGrammar
	GenerateLanguage = grammargen.GenerateLanguage
)

type Grammar = grammargen.Grammar
type Rule = grammargen.Rule

// ArbiterGrammar defines the arbiter rule engine DSL.
func ArbiterGrammar() *Grammar {
	g := NewGrammar("arbiter")

	// --- Source file ---
	g.Define("source_file", Repeat(
		Sym("_declaration"),
	))

	g.Define("_declaration", Choice(
		Sym("include_declaration"),
		Sym("feature_declaration"),
		Sym("const_declaration"),
		Sym("rule_declaration"),
		Sym("expert_rule_declaration"),
		Sym("segment_declaration"),
		Sym("flag_declaration"),
	))

	// --- Comments (extras — auto-skipped) ---
	g.Define("comment", Token(Pat(`#[^\n]*`)))

	g.Define("include_declaration", Seq(
		Str("include"),
		Field("path", Sym("string_literal")),
	))

	// --- Feature declaration ---
	g.Define("feature_declaration", Seq(
		Str("feature"),
		Field("name", Sym("identifier")),
		Str("from"),
		Field("source", Sym("string_literal")),
		Str("{"),
		Repeat(Sym("field_declaration")),
		Str("}"),
	))

	g.Define("field_declaration", Seq(
		Field("name", Sym("identifier")),
		Str(":"),
		Field("type", Sym("type_name")),
	))

	g.Define("type_name", Choice(
		Str("number"),
		Str("string"),
		Str("bool"),
		Seq(Str("list"), Str("<"), Sym("type_name"), Str(">")),
	))

	// --- Const declaration ---
	g.Define("const_declaration", Seq(
		Str("const"),
		Field("name", Sym("identifier")),
		Str("="),
		Field("value", Sym("_expr")),
	))

	// --- Rule declaration ---
	g.Define("rule_declaration", Seq(
		Str("rule"),
		Field("name", Sym("identifier")),
		Optional(Seq(Str("priority"), Field("priority", Sym("number_literal")))),
		Str("{"),
		Optional(Field("kill_switch", Sym("kill_switch"))),
		Repeat(Sym("rule_requires")),
		Field("condition", Sym("when_block")),
		Field("action", Sym("then_block")),
		Optional(Field("fallback", Sym("otherwise_block"))),
		Optional(Field("rollout", Sym("rule_rollout"))),
		Str("}"),
	))

	g.Define("rule_requires", Seq(
		Str("requires"),
		Field("name", Sym("identifier")),
	))

	g.Define("rule_rollout", Seq(
		Str("rollout"),
		Field("value", Sym("number_literal")),
	))

	g.Define("expert_rule_declaration", Seq(
		Str("expert"),
		Str("rule"),
		Field("name", Sym("identifier")),
		Optional(Seq(Str("priority"), Field("priority", Sym("number_literal")))),
		Str("{"),
		Optional(Field("kill_switch", Sym("kill_switch"))),
		Optional(Field("no_loop", Sym("no_loop"))),
		Repeat(Sym("rule_requires")),
		Optional(Field("activation_group", Sym("expert_activation_group"))),
		Field("condition", Sym("expert_when_block")),
		Field("action", Sym("expert_then_block")),
		Optional(Field("rollout", Sym("rule_rollout"))),
		Str("}"),
	))

	// when always requires braces: when { expr }
	g.Define("when_block", Seq(
		Str("when"),
		Optional(Seq(
			Str("segment"),
			Field("segment", Sym("identifier")),
		)),
		Str("{"),
		Field("expr", Sym("_expr")),
		Str("}"),
	))

	g.Define("expert_when_block", Seq(
		Str("when"),
		Optional(Seq(
			Str("segment"),
			Field("segment", Sym("identifier")),
		)),
		Choice(
			Seq(Str("{"), Field("expr", Sym("_expr")), Str("}")),
			Seq(Str("{"), Field("bindings", Sym("expert_binding_clause")), Str("}")),
		),
	))

	g.Define("expert_binding_clause", Seq(
		Repeat1(Sym("expert_binding")),
		Field("where", Sym("expert_where_block")),
	))

	g.Define("expert_binding", Seq(
		Str("bind"),
		Field("name", Sym("identifier")),
		Str("in"),
		Field("source", Sym("_value_expr")),
	))

	g.Define("expert_where_block", Seq(
		Str("where"),
		Str("{"),
		Field("expr", Sym("_expr")),
		Str("}"),
	))

	g.Define("then_block", Seq(
		Str("then"),
		Field("action_name", Sym("identifier")),
		Str("{"),
		Repeat(Sym("param_assignment")),
		Str("}"),
	))

	g.Define("otherwise_block", Seq(
		Str("otherwise"),
		Field("action_name", Sym("identifier")),
		Str("{"),
		Repeat(Sym("param_assignment")),
		Str("}"),
	))

	g.Define("expert_then_block", Seq(
		Str("then"),
		Field("kind", Choice(Str("assert"), Str("emit"), Str("retract"), Str("modify"))),
		Field("action_name", Sym("identifier")),
		Str("{"),
		Repeat(Sym("param_assignment")),
		Optional(Field("set_block", Sym("expert_set_block"))),
		Str("}"),
	))

	g.Define("expert_set_block", Seq(
		Str("set"),
		Str("{"),
		Repeat(Sym("param_assignment")),
		Str("}"),
	))

	g.Define("no_loop", Str("no_loop"))

	g.Define("expert_activation_group", Seq(
		Str("activation_group"),
		Field("name", Sym("identifier")),
	))

	g.Define("param_assignment", Seq(
		Field("key", Sym("identifier")),
		Str(":"),
		Field("value", Sym("_expr")),
		Optional(Str(",")),
	))

	// --- Segment declaration ---
	g.Define("segment_declaration", Seq(
		Str("segment"),
		Field("name", Sym("identifier")),
		Str("{"),
		Field("condition", Sym("_expr")),
		Str("}"),
	))

	// --- Flag declaration ---
	g.Define("flag_declaration", Seq(
		Str("flag"),
		Field("name", Sym("identifier")),
		Optional(Seq(Str("type"), Field("flag_type", Choice(Str("boolean"), Str("multivariate"))))),
		Optional(Seq(Str("default"), Field("default_value", Sym("_primary")))),
		Optional(Field("kill_switch", Sym("kill_switch"))),
		Str("{"),
		Repeat(Sym("_flag_body")),
		Str("}"),
	))

	g.Define("_flag_body", Choice(
		Sym("flag_metadata"),
		Sym("flag_requires"),
		Sym("flag_rule"),
		Sym("variant_declaration"),
		Sym("defaults_block"),
	))

	// variant "treatment" { key: value, ... }
	g.Define("variant_declaration", Seq(
		Str("variant"),
		Field("name", Sym("string_literal")),
		Str("{"),
		Repeat(Sym("param_assignment")),
		Str("}"),
	))

	// defaults { key: value, ... } — inherited by all variants
	g.Define("defaults_block", Seq(
		Str("defaults"),
		Str("{"),
		Repeat(Sym("param_assignment")),
		Str("}"),
	))

	// owner: "oscar", ticket: "ENG-1234", etc.
	g.Define("flag_metadata", Seq(
		Field("key", Sym("identifier")),
		Str(":"),
		Field("value", Sym("string_literal")),
	))

	// requires payments_enabled
	g.Define("flag_requires", Seq(
		Str("requires"),
		Field("flag_name", Sym("identifier")),
	))

	// when segment_name [rollout N] then "variant"
	// OR when { expr } [rollout N] then "variant"
	g.Define("flag_rule", Seq(
		Str("when"),
		Field("condition", Choice(
			Sym("identifier"),                     // segment reference
			Seq(Str("{"), Sym("_expr"), Str("}")), // inline condition
		)),
		Optional(Seq(Str("rollout"), Field("rollout", Sym("number_literal")))),
		Str("then"),
		Field("variant", Sym("_primary")),
	))

	// kill_switch as named node so it appears in the CST
	g.Define("kill_switch", Str("kill_switch"))

	// --- Expression hierarchy ---
	// Two levels: _expr includes logical operators, _value_expr does not.
	// Comparisons only accept _value_expr on left/right so that
	// `a >= 18 and b == true` parses as `(a >= 18) and (b == true)`.

	g.Define("_expr", Choice(
		Sym("or_expr"),
		Sym("and_expr"),
		Sym("not_expr"),
		Sym("_cond_expr"),
	))

	// _cond_expr: comparisons and operators (no logical)
	g.Define("_cond_expr", Choice(
		Sym("comparison_expr"),
		Sym("in_expr"),
		Sym("not_in_expr"),
		Sym("contains_expr"),
		Sym("not_contains_expr"),
		Sym("retains_expr"),
		Sym("not_retains_expr"),
		Sym("subset_of_expr"),
		Sym("superset_of_expr"),
		Sym("vague_contains_expr"),
		Sym("starts_with_expr"),
		Sym("ends_with_expr"),
		Sym("matches_expr"),
		Sym("between_expr"),
		Sym("is_null_expr"),
		Sym("is_not_null_expr"),
		Sym("quantifier_expr"),
		Sym("_value_expr"),
	))

	// _value_expr: math and primaries only
	g.Define("_value_expr", Choice(
		Sym("math_expr"),
		Sym("_primary"),
	))

	// Logical — operate on _expr (full expressions)
	g.Define("or_expr", PrecLeft(1, Seq(
		Field("left", Sym("_expr")),
		Str("or"),
		Field("right", Sym("_expr")),
	)))

	g.Define("and_expr", PrecLeft(2, Seq(
		Field("left", Sym("_expr")),
		Str("and"),
		Field("right", Sym("_expr")),
	)))

	g.Define("not_expr", PrecRight(3, Seq(
		Str("not"),
		Field("operand", Sym("_expr")),
	)))

	// Comparison — left/right are _value_expr (no logical operators)
	g.Define("_ge_op", Token(Seq(Str(">"), Str("="))))
	g.Define("_le_op", Token(Seq(Str("<"), Str("="))))
	g.Define("_ne_op", Token(Seq(Str("!"), Str("="))))

	g.Define("comparison_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")),
		Field("op", Choice(
			Str("=="), Sym("_ne_op"),
			Sym("_ge_op"), Sym("_le_op"),
			Str(">"), Str("<"),
		)),
		Field("right", Sym("_value_expr")),
	)))

	// Collection operators — operands are _value_expr
	g.Define("in_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("in"), Field("right", Sym("_value_expr")),
	)))
	g.Define("not_in_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("not"), Str("in"), Field("right", Sym("_value_expr")),
	)))
	g.Define("contains_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("contains"), Field("right", Sym("_value_expr")),
	)))
	g.Define("not_contains_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("not"), Str("contains"), Field("right", Sym("_value_expr")),
	)))
	g.Define("retains_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("retains"), Field("right", Sym("_value_expr")),
	)))
	g.Define("not_retains_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("not"), Str("retains"), Field("right", Sym("_value_expr")),
	)))
	g.Define("subset_of_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("subset_of"), Field("right", Sym("_value_expr")),
	)))
	g.Define("superset_of_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("superset_of"), Field("right", Sym("_value_expr")),
	)))
	g.Define("vague_contains_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("vague_contains"), Field("right", Sym("_value_expr")),
	)))

	// String operators — operands are _value_expr
	g.Define("starts_with_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("starts_with"), Field("right", Sym("_value_expr")),
	)))
	g.Define("ends_with_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("ends_with"), Field("right", Sym("_value_expr")),
	)))
	g.Define("matches_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("matches"), Field("right", Sym("_value_expr")),
	)))

	// Range: x between [1, 10]
	g.Define("between_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")),
		Str("between"),
		Field("open", Choice(Str("["), Str("("))),
		Field("low", Sym("_value_expr")),
		Str(","),
		Field("high", Sym("_value_expr")),
		Field("close", Choice(Str("]"), Str(")"))),
	)))

	// Null checks — left is _value_expr
	g.Define("is_null_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("is"), Str("null"),
	)))
	g.Define("is_not_null_expr", PrecLeft(4, Seq(
		Field("left", Sym("_value_expr")), Str("is"), Str("not"), Str("null"),
	)))

	// Math — operands are _value_expr (binds tighter than comparisons)
	g.Define("math_expr", PrecLeft(5, Seq(
		Field("left", Sym("_value_expr")),
		Field("op", Choice(Str("+"), Str("-"), Str("*"), Str("/"), Str("%"))),
		Field("right", Sym("_value_expr")),
	)))

	// Quantifiers: any/all/none x in collection { body }
	g.Define("quantifier_expr", Seq(
		Field("quantifier", Choice(Str("any"), Str("all"), Str("none"))),
		Field("var", Sym("identifier")),
		Str("in"),
		Field("collection", Sym("_expr")),
		Str("{"),
		Field("body", Sym("_expr")),
		Str("}"),
	))

	// --- Primaries ---
	g.Define("_primary", Choice(
		Sym("member_expr"),
		Sym("number_literal"),
		Sym("string_literal"),
		Sym("bool_literal"),
		Sym("list_literal"),
		Sym("paren_expr"),
		Sym("secret_ref"),
		Sym("identifier"),
	))

	// secret("path/to/secret") — resolved at runtime, never stored in plaintext
	g.Define("secret_ref", Seq(
		Str("secret"),
		Str("("),
		Field("ref", Sym("string_literal")),
		Str(")"),
	))

	g.Define("member_expr", PrecLeft(8, Seq(
		Field("object", Choice(Sym("member_expr"), Sym("identifier"))),
		Str("."),
		Field("field", Sym("identifier")),
	)))

	g.Define("paren_expr", Seq(
		Str("("), Field("expr", Sym("_expr")), Str(")"),
	))

	g.Define("list_literal", Seq(
		Str("["), CommaSep1(Sym("_expr")), Optional(Str(",")), Str("]"),
	))

	// --- Terminals ---
	g.Define("identifier", Pat(`[a-zA-Z_][a-zA-Z0-9_]*`))
	g.Define("number_literal", Pat(`-?[0-9]+(\.[0-9]+)?`))
	g.Define("string_literal", Pat(`"[^"]*"`))
	g.Define("bool_literal", Choice(Str("true"), Str("false")))

	// Extras: whitespace and comments
	g.SetExtras(Pat(`[ \t\r\n]+`), Sym("comment"))

	g.SetWord("identifier")

	// --- Conflicts ---
	g.SetConflicts(
		// Logical level
		[]string{"_expr", "or_expr"},
		[]string{"_expr", "and_expr"},
		[]string{"_expr", "not_expr"},
		[]string{"_expr", "_cond_expr"},
		// Condition level
		[]string{"_cond_expr", "_value_expr"},
		// Value level
		[]string{"_value_expr", "math_expr"},
		// not ambiguity: not_expr vs not_in/not_contains/not_retains
		[]string{"not_expr", "not_in_expr"},
		[]string{"not_expr", "not_contains_expr"},
		[]string{"not_expr", "not_retains_expr"},
		[]string{"not_expr", "is_not_null_expr"},
		// between brackets vs list
		[]string{"between_expr", "list_literal"},
	)

	g.EnableLRSplitting = true

	return g
}
