;; Arbiter rule engine DSL highlights

;; Literals
(comment) @comment
(string_literal) @string
(number_literal) @number
(bool_literal) @constant.builtin

;; Identifiers
(identifier) @variable

;; Includes
(include_declaration "include" @keyword)
(include_declaration path: (string_literal) @string.special.path)

;; Declarations
(feature_declaration "feature" @keyword)
(feature_declaration name: (identifier) @type.definition)
(feature_declaration "from" @keyword)

(field_declaration name: (identifier) @property)

(const_declaration "const" @keyword)
(const_declaration name: (identifier) @constant)

(segment_declaration "segment" @keyword)
(segment_declaration name: (identifier) @type.definition)

(rule_declaration "rule" @keyword)
(rule_declaration name: (identifier) @function)
(rule_declaration "priority" @keyword)
(rule_declaration "requires" @keyword)
(rule_declaration "rollout" @keyword)
(rule_declaration "kill_switch" @keyword)

(expert_rule_declaration "expert" @keyword)
(expert_rule_declaration "rule" @keyword)
(expert_rule_declaration name: (identifier) @function)
(expert_rule_declaration "priority" @keyword)
(expert_rule_declaration "requires" @keyword)
(expert_rule_declaration "rollout" @keyword)
(expert_rule_declaration "no_loop" @keyword)
(expert_rule_declaration "activation_group" @keyword)

(flag_declaration "flag" @keyword)
(flag_declaration name: (identifier) @type.definition)
(flag_declaration "type" @keyword)
(flag_declaration "default" @keyword)
(flag_declaration "kill_switch" @keyword)

(variant_declaration "variant" @keyword)
(variant_declaration name: (string_literal) @string)
(defaults_block "defaults" @keyword)
(flag_requires "requires" @keyword)
(flag_requires flag_name: (identifier) @function)
(flag_rule "when" @keyword)
(flag_rule "then" @keyword)
(flag_rule "rollout" @keyword)

;; Blocks
(when_block "when" @keyword)
(when_block "segment" @keyword)
(then_block "then" @keyword)
(then_block action_name: (identifier) @function)
(otherwise_block "otherwise" @keyword)
(otherwise_block action_name: (identifier) @function)

(expert_then_block "then" @keyword)
(expert_then_block kind: (identifier) @keyword)
(expert_then_block action_name: (identifier) @function)
(expert_set_block "set" @keyword)

(param_assignment key: (identifier) @property)
(flag_metadata key: (identifier) @property)

;; Logical operators
"or" @keyword
"and" @keyword
"not" @keyword

;; Collection operators
"in" @keyword
"contains" @keyword
"retains" @keyword
"subset_of" @keyword
"superset_of" @keyword
"vague_contains" @keyword
"starts_with" @keyword
"ends_with" @keyword
"matches" @keyword
"between" @keyword
"is" @keyword
"null" @constant.builtin

;; Quantifiers
"any" @keyword
"all" @keyword
"none" @keyword

;; Expert actions
"assert" @keyword
"emit" @keyword
"retract" @keyword
"modify" @keyword
"bind" @keyword
"where" @keyword
"set" @keyword

;; Types
"number" @type.builtin
"string" @type.builtin
"bool" @type.builtin
"list" @type.builtin

;; Operators
"==" @operator
"!=" @operator
">=" @operator
"<=" @operator
">" @operator
"<" @operator
"=" @operator
":" @operator
"+" @operator
"-" @operator
"*" @operator
"/" @operator
"%" @operator
"." @operator
