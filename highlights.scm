;; Arbiter rule engine DSL highlights

;; Literals
(comment) @comment
(string_literal) @string
(slack_channel_literal) @string.special
(resource_literal) @string.special
(number_literal) @number
(duration_literal) @number
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

(strategy_declaration "strategy" @keyword)
(strategy_declaration "returns" @keyword)
(strategy_declaration name: (identifier) @function)
(strategy_declaration returns: (identifier) @type)
(strategy_when_candidate "then" @keyword)
(strategy_when_candidate action_name: (identifier) @function)
(strategy_else_candidate "else" @keyword)
(strategy_else_candidate action_name: (identifier) @function)

(arbiter_declaration "arbiter" @keyword)
(arbiter_declaration name: (identifier) @type.definition)
(arbiter_poll_clause "poll" @keyword)
(arbiter_stream_clause "stream" @keyword)
(arbiter_schedule_clause "schedule" @keyword)
(arbiter_schedule_clause "source" @keyword)
(arbiter_source_clause "source" @keyword)
(arbiter_checkpoint_clause "checkpoint" @keyword)
(arbiter_handler_clause "on" @keyword)
(arbiter_handler_filter "where" @keyword)
(arbiter_handler_kind) @keyword
(arbiter_wildcard) @operator

(segment_declaration "segment" @keyword)
(segment_declaration name: (identifier) @type.definition)

(rule_declaration "rule" @keyword)
(rule_declaration name: (identifier) @function)
(rule_declaration "priority" @keyword)

(expert_rule_declaration "expert" @keyword)
(expert_rule_declaration "rule" @keyword)
(expert_rule_declaration name: (identifier) @function)
(expert_rule_declaration "priority" @keyword)

;; Governance nodes (named child nodes, not anonymous strings)
(kill_switch) @keyword
(no_loop) @keyword
(stable) @keyword
(rule_requires "requires" @keyword)
(rule_requires name: (identifier) @function)
(rule_excludes "excludes" @keyword)
(rule_excludes name: (identifier) @function)
(rule_rollout "rollout" @keyword)
(expert_activation_group "activation_group" @keyword)
(expert_activation_group name: (identifier) @type)

(flag_declaration "flag" @keyword)
(flag_declaration name: (identifier) @type.definition)
(flag_declaration "type" @keyword)
(flag_declaration "boolean" @type.builtin)
(flag_declaration "multivariate" @type.builtin)
(flag_declaration "default" @keyword)

(variant_declaration "variant" @keyword)
(variant_declaration name: (string_literal) @string)
(defaults_block "defaults" @keyword)
(flag_requires "requires" @keyword)
(flag_requires flag_name: (identifier) @function)
(flag_rule "when" @keyword)
(flag_rule "else" @keyword)
(flag_rule "then" @keyword)
(flag_rule "rollout" @keyword)

;; Blocks
(when_block "when" @keyword)
(when_block "segment" @keyword)
(expert_when_block "when" @keyword)
(expert_when_block "segment" @keyword)
(expert_where_block "where" @keyword)
(expert_binding "bind" @keyword)
(expert_binding "in" @keyword)
(expert_binding name: (identifier) @variable.parameter)
(let_binding "let" @keyword)
(let_binding name: (identifier) @variable.parameter)
(then_block "then" @keyword)
(then_block action_name: (identifier) @function)
(otherwise_block "otherwise" @keyword)
(otherwise_block action_name: (identifier) @function)

(expert_then_block "then" @keyword)
(expert_then_block "assert" @keyword)
(expert_then_block "emit" @keyword)
(expert_then_block "retract" @keyword)
(expert_then_block "modify" @keyword)
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
(aggregate_expr function: (identifier) @keyword
  (#match? @keyword "^(sum|count|avg)$"))

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
