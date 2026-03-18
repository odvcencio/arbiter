;; Arbiter rule engine DSL highlights

;; Literals
(comment) @comment
(string_literal) @string
(number_literal) @number
(bool_literal) @constant.builtin

;; Identifiers
(identifier) @variable

;; Declarations
(feature_declaration "feature" @keyword)
(feature_declaration name: (identifier) @type.definition)
(feature_declaration "from" @keyword)

(field_declaration name: (identifier) @property)

(const_declaration "const" @keyword)
(const_declaration name: (identifier) @constant)

(rule_declaration "rule" @keyword)
(rule_declaration name: (identifier) @function)
(rule_declaration "priority" @keyword)

;; Blocks
(when_block "when" @keyword)
(then_block "then" @keyword)
(then_block action_name: (identifier) @function)
(otherwise_block "otherwise" @keyword)
(otherwise_block action_name: (identifier) @function)

(param_assignment key: (identifier) @property)

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

;; Types
"number" @type.builtin
"string" @type.builtin
"bool" @type.builtin
"list" @type.builtin

;; Operators
"==" @operator
">" @operator
"<" @operator
":" @operator
"+" @operator
"-" @operator
"*" @operator
"/" @operator
"%" @operator
"." @operator
