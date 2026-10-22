package ir

import "regexp"

// ExprID indexes into Program.Exprs.
type ExprID uint32

// Program is the lowered in-process representation of a parsed `.arb` source.
type Program struct {
	Imports        []Import
	Tags           []TagDeclaration
	Input          *InputSchema
	InputRef       *InputRef
	Consts         []Const
	Templates      []Template
	Features       []Feature
	FactSchemas    []FactSchema
	OutcomeSchemas []OutcomeSchema
	Strategies     []Strategy
	Workers        []Worker
	Segments       []Segment
	Rules          []Rule
	Flags          []Flag
	Expert         []ExpertRule
	Arbiters       []Arbiter
	Tables         []Table
	Exprs          []Expr

	// ValidatedRegexes holds pre-compiled regexes for literal string patterns
	// validated at compile time. Keyed by pattern string.
	ValidatedRegexes map[string]*regexp.Regexp

	tagIndex           map[string]int
	constIndex         map[string]int
	factSchemaIndex    map[string]int
	outcomeSchemaIndex map[string]int
	strategyIndex      map[string]int
	workerIndex        map[string]int
	segmentIndex       map[string]int
	ruleIndex          map[string]int
	flagIndex          map[string]int
	expertIndex        map[string]int
	arbiterIndex       map[string]int
	tableIndex         map[string]int
}

// TagDeclaration is one top-level tag declaration.
type TagDeclaration struct {
	Name string
	Span Span
}

// Span stores byte and point ranges for a declaration or expression.
type Span struct {
	StartByte uint32 `json:"start_byte"`
	EndByte   uint32 `json:"end_byte"`
	StartRow  uint32 `json:"start_row"`
	StartCol  uint32 `json:"start_col"`
	EndRow    uint32 `json:"end_row"`
	EndCol    uint32 `json:"end_col"`
}

// Const is one top-level const declaration.
type Const struct {
	Name  string
	Span  Span
	Value ExprID
}

// Template is a parameterized expression macro. Its body is lowered once with
// parameters represented as ExprParamRef placeholders; each call site is
// resolved by cloning the body and substituting arguments for the placeholders.
type Template struct {
	Name   string
	Params []string
	Body   ExprID
	Span   Span
}

// Feature is one top-level feature declaration.
type Feature struct {
	Name   string
	Source string
	Span   Span
	Fields []FeatureField
}

// FeatureField is one feature field declaration.
type FeatureField struct {
	Name string
	Type string
	Span Span
}

// FactSchema is one top-level fact schema declaration.
type FactSchema struct {
	Name   string        `json:"name"`
	Fields []SchemaField `json:"fields"`
	Span   Span          `json:"span"`
}

// OutcomeSchema is one top-level outcome schema declaration.
type OutcomeSchema struct {
	Name   string        `json:"name"`
	Fields []SchemaField `json:"fields"`
	Span   Span          `json:"span"`
}

// SchemaField is one fact/outcome schema field declaration.
type SchemaField struct {
	Name     string        `json:"name"`
	Type     FieldType     `json:"type"`
	Required bool          `json:"required"`
	Children []SchemaField `json:"children"` // for nested objects in input schemas
	Span     Span          `json:"span"`
}

// FieldType is one schema field type.
type FieldType struct {
	Base      string     `json:"base"`      // "string", "number", "decimal", "boolean", "timestamp", "list", "object"
	Dimension string     `json:"dimension"` // for number<D>, decimal<D>
	Element   *FieldType `json:"element"`   // for list<T> — element type; nil for non-list types
	// Open marks an object whose keys are not statically known (e.g. a protobuf
	// map or an unresolved/imported message type). Access to any sub-path under
	// an open object resolves to an unknown (permissive) type rather than a
	// "field not declared" error, even under a closed input schema.
	Open bool `json:"open"`
}

// InputRef is an external input-schema reference declared in source as
// `input from proto "<path>" message "<name>"`. It is resolved to an
// InputSchema at compile time, relative to the source file's directory.
type InputRef struct {
	Kind    string // "proto"
	Path    string
	Message string
	Span    Span
}

// InputSchema is the top-level input schema declaration.
type InputSchema struct {
	Fields []SchemaField
	Span   Span
	// Closed reports whether references to undeclared top-level fields are an
	// error. An in-source input{} block is open (false) for backward
	// compatibility; a schema bound from an external source (e.g. a .proto via
	// WithInputSchema) is closed (true) so typo'd field names are caught.
	Closed bool
}

// Import is one top-level import declaration.
type Import struct {
	Path  string // e.g., "fraud/scoring"
	Alias string // e.g., "fs", or empty for default (last path segment)
	Span  Span
}

// Segment is one top-level segment declaration.
type Segment struct {
	Name      string
	Span      Span
	Condition ExprID
}

// KillSwitchState records whether a declaration omitted kill_switch, enabled it,
// or explicitly set it off.
type KillSwitchState string

const (
	KillSwitchUnset KillSwitchState = ""
	KillSwitchOn    KillSwitchState = "on"
	KillSwitchOff   KillSwitchState = "off"
)

// IsSet reports whether the declaration mentioned kill_switch explicitly.
func (s KillSwitchState) IsSet() bool {
	return s != KillSwitchUnset
}

// Enabled reports whether the declaration enables the kill switch.
func (s KillSwitchState) Enabled() bool {
	return s == KillSwitchOn
}

// Rule is one top-level standard rule declaration.
type Rule struct {
	Name         string
	Span         Span
	Tags         []string
	Priority     int32
	KillSwitch   KillSwitchState
	ActiveWindow ActiveWindow
	Prereqs      []string
	Excludes     []string
	Segment      string
	Lets         []LetBinding
	Condition    ExprID
	HasCondition bool
	Action       Action
	Fallback     *Action
	Rollout      *Rollout
}

// Strategy is one top-level strategy declaration.
type Strategy struct {
	Name       string
	Returns    string
	Span       Span
	Candidates []StrategyCandidate
}

// StrategyCandidate is one ordered arm in a strategy declaration.
type StrategyCandidate struct {
	Label        string
	Span         Span
	Segment      string
	Lets         []LetBinding
	Condition    ExprID
	HasCondition bool
	KillSwitch   KillSwitchState
	ActiveWindow ActiveWindow
	Rollout      *Rollout
	Params       []ActionParam
	IsElse       bool
}

// Worker is one top-level worker capability declaration.
type Worker struct {
	Name   string
	Input  string
	Output string
	Kind   string
	Target string
	Span   Span
}

// FlagType identifies the kind of flag declaration.
type FlagType string

const (
	FlagBoolean      FlagType = "boolean"
	FlagMultivariate FlagType = "multivariate"
)

// Flag is one top-level flag declaration.
type Flag struct {
	Name       string
	Span       Span
	Tags       []string
	Type       FlagType
	Default    string
	KillSwitch KillSwitchState
	Requires   []string
	Rules      []FlagRule
	Variants   []Variant
	Defaults   []ActionParam
	Metadata   []MetadataEntry
}

// MetadataEntry is one key/value metadata pair.
type MetadataEntry struct {
	Key   string
	Value string
	Span  Span
}

// FlagRule is one targeting rule inside a flag declaration.
type FlagRule struct {
	Span         Span
	Segment      string
	Condition    ExprID
	HasCondition bool
	ActiveWindow ActiveWindow
	Rollout      *Rollout
	Variant      string
	Split        *FlagSplit
	IsElse       bool
}

// ActiveWindow is one stateless temporal eligibility window.
type ActiveWindow struct {
	From      string
	Until     string
	HasFrom   bool
	HasUntil  bool
	FromSpan  Span
	UntilSpan Span
}

// Enabled reports whether either active window bound was declared.
func (w ActiveWindow) Enabled() bool {
	return w.HasFrom || w.HasUntil
}

// FlagSplit is one weighted variant split block.
type FlagSplit struct {
	Subject      string
	Namespace    string
	HasSubject   bool
	HasNamespace bool
	Weights      []FlagSplitWeight
}

// FlagSplitWeight is one weighted split entry.
type FlagSplitWeight struct {
	Variant string
	Weight  uint16
	Span    Span
}

// Variant is one declared multivariate flag variant.
type Variant struct {
	Name   string
	Span   Span
	Params []ActionParam
}

// ExpertActionKind identifies the kind of expert-rule action.
type ExpertActionKind string

const (
	ExpertAssert  ExpertActionKind = "assert"
	ExpertEmit    ExpertActionKind = "emit"
	ExpertRetract ExpertActionKind = "retract"
	ExpertModify  ExpertActionKind = "modify"
)

// ExpertRule is one top-level expert rule declaration.
type ExpertRule struct {
	Name             string
	Span             Span
	Tags             []string
	Priority         int32
	KillSwitch       KillSwitchState
	Prereqs          []string
	Excludes         []string
	Segment          string
	Lets             []LetBinding
	Condition        ExprID
	HasCondition     bool
	Rollout          *Rollout
	PerFact          bool
	NoLoop           bool
	Stable           bool
	ActivationGroup  string
	ForDuration      *Duration
	WithinDuration   *Duration
	StableCycles     int
	HasStableCycles  bool
	CooldownDuration *Duration
	DebounceDuration *Duration
	ActionKind       ExpertActionKind
	Target           string
	Params           []ActionParam
	SetParams        []ActionParam
}

// Duration is one parsed temporal duration literal.
type Duration struct {
	Value float64
	Unit  string
	Span  Span
}

// ArbiterClauseKind identifies the kind of arbiter clause.
type ArbiterClauseKind string

const (
	ArbiterPollClause       ArbiterClauseKind = "poll"
	ArbiterStreamClause     ArbiterClauseKind = "stream"
	ArbiterScheduleClause   ArbiterClauseKind = "schedule"
	ArbiterSourceClause     ArbiterClauseKind = "source"
	ArbiterCheckpointClause ArbiterClauseKind = "checkpoint"
	ArbiterHandlerClause    ArbiterClauseKind = "handler"
)

// Arbiter is one top-level arbiter declaration.
type Arbiter struct {
	Name    string
	Span    Span
	Clauses []ArbiterClause
}

// ArbiterClause is one clause in an arbiter declaration.
type ArbiterClause struct {
	Kind      ArbiterClauseKind
	Span      Span
	Interval  string
	Expr      string
	Target    string
	Outcome   string
	Handler   string
	Filter    ExprID
	HasFilter bool
}

// LetBinding is one `let name = expr` binding inside a `when` block.
type LetBinding struct {
	Name  string
	Span  Span
	Value ExprID
}

// Action is one `then` or `otherwise` block.
type Action struct {
	Name   string
	Span   Span
	Lets   []LetBinding
	Params []ActionParam
}

// ActionParam is one action parameter assignment.
type ActionParam struct {
	Key   string
	Span  Span
	Value ExprID
}

// Rollout is one rollout specification.
type Rollout struct {
	Span         Span
	Bps          uint16
	HasBps       bool
	Subject      string
	Namespace    string
	HasSubject   bool
	HasNamespace bool
}

// ExprKind identifies the kind of expression node.
type ExprKind string

const (
	ExprStringLit    ExprKind = "string_lit"
	ExprNumberLit    ExprKind = "number_lit"
	ExprQuantityLit  ExprKind = "quantity_lit"
	ExprDecimalLit   ExprKind = "decimal_lit"
	ExprTimestampLit ExprKind = "timestamp_lit"
	ExprBoolLit      ExprKind = "bool_lit"
	ExprNullLit      ExprKind = "null_lit"
	ExprListLit      ExprKind = "list_lit"

	ExprVarRef    ExprKind = "var_ref"
	ExprConstRef  ExprKind = "const_ref"
	ExprLocalRef  ExprKind = "local_ref"
	ExprSecretRef ExprKind = "secret_ref"
	ExprParamRef  ExprKind = "param_ref" // template parameter placeholder, substituted at inline time

	ExprBinary      ExprKind = "binary"
	ExprUnary       ExprKind = "unary"
	ExprBetween     ExprKind = "between"
	ExprQuantifier  ExprKind = "quantifier"
	ExprAggregate   ExprKind = "aggregate"
	ExprBuiltinCall ExprKind = "builtin_call"
	ExprLookup      ExprKind = "lookup"
)

// BinaryOpKind identifies a binary operator.
type BinaryOpKind string

const (
	BinaryEq            BinaryOpKind = "=="
	BinaryNeq           BinaryOpKind = "!="
	BinaryGt            BinaryOpKind = ">"
	BinaryGte           BinaryOpKind = ">="
	BinaryLt            BinaryOpKind = "<"
	BinaryLte           BinaryOpKind = "<="
	BinaryAnd           BinaryOpKind = "and"
	BinaryOr            BinaryOpKind = "or"
	BinaryIn            BinaryOpKind = "in"
	BinaryNotIn         BinaryOpKind = "not_in"
	BinaryContains      BinaryOpKind = "contains"
	BinaryNotContains   BinaryOpKind = "not_contains"
	BinaryRetains       BinaryOpKind = "retains"
	BinaryNotRetains    BinaryOpKind = "not_retains"
	BinarySubsetOf      BinaryOpKind = "subset_of"
	BinarySupersetOf    BinaryOpKind = "superset_of"
	BinaryVagueContains BinaryOpKind = "vague_contains"
	BinaryStartsWith    BinaryOpKind = "starts_with"
	BinaryEndsWith      BinaryOpKind = "ends_with"
	BinaryMatches       BinaryOpKind = "matches"
	BinaryAdd           BinaryOpKind = "+"
	BinarySub           BinaryOpKind = "-"
	BinaryMul           BinaryOpKind = "*"
	BinaryDiv           BinaryOpKind = "/"
	BinaryMod           BinaryOpKind = "%"
)

// UnaryOpKind identifies a unary operator.
type UnaryOpKind string

const (
	UnaryNot       UnaryOpKind = "not"
	UnaryIsNull    UnaryOpKind = "is_null"
	UnaryIsNotNull UnaryOpKind = "is_not_null"
)

// BetweenKind identifies the kind of between expression.
type BetweenKind string

const (
	BetweenClosedClosed BetweenKind = "[]"
	BetweenOpenOpen     BetweenKind = "()"
	BetweenClosedOpen   BetweenKind = "[)"
	BetweenOpenClosed   BetweenKind = "(]"
)

// QuantifierKind identifies a quantifier expression.
type QuantifierKind string

const (
	QuantifierAny  QuantifierKind = "any"
	QuantifierAll  QuantifierKind = "all"
	QuantifierNone QuantifierKind = "none"
)

// AggregateKind identifies an aggregate expression.
type AggregateKind string

const (
	AggregateSum   AggregateKind = "sum"
	AggregateCount AggregateKind = "count"
	AggregateAvg   AggregateKind = "avg"
)

// Expr is one lowered expression node.
type Expr struct {
	Kind ExprKind
	Span Span

	String string
	Number float64
	Bool   bool
	Name   string
	Path   string
	Unit   string

	Elems []ExprID

	BinaryOp BinaryOpKind
	Left     ExprID
	Right    ExprID

	UnaryOp UnaryOpKind
	Operand ExprID

	BetweenKind BetweenKind
	Value       ExprID
	Low         ExprID
	High        ExprID

	QuantifierKind QuantifierKind
	AggregateKind  AggregateKind
	VarName        string
	Collection     ExprID
	Body           ExprID
	ValueExpr      ExprID
	HasValueExpr   bool

	FuncName string
	Args     []ExprID

	// Lookup expression fields (ExprLookup)
	TableName string
	Where     ExprID // 0 if no where clause
	SortCol   string
	SortDesc  bool
	ElseKeys  []string
	ElseVals  []ExprID
}

// Expr returns the expression at id.
func (p *Program) Expr(id ExprID) *Expr {
	if p == nil {
		return nil
	}
	idx := int(id)
	if idx < 0 || idx >= len(p.Exprs) {
		return nil
	}
	return &p.Exprs[idx]
}

// TagByName returns the named tag declaration.
func (p *Program) TagByName(name string) (*TagDeclaration, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.tagIndex[name]; ok {
		return &p.Tags[idx], true
	}
	return nil, false
}

// ConstByName returns the named const declaration.
func (p *Program) ConstByName(name string) (*Const, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.constIndex[name]; ok {
		return &p.Consts[idx], true
	}
	return nil, false
}

// SegmentByName returns the named segment declaration.
func (p *Program) SegmentByName(name string) (*Segment, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.segmentIndex[name]; ok {
		return &p.Segments[idx], true
	}
	return nil, false
}

// FactSchemaByName returns the named fact schema declaration.
func (p *Program) FactSchemaByName(name string) (*FactSchema, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.factSchemaIndex[name]; ok {
		return &p.FactSchemas[idx], true
	}
	return nil, false
}

// OutcomeSchemaByName returns the named outcome schema declaration.
func (p *Program) OutcomeSchemaByName(name string) (*OutcomeSchema, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.outcomeSchemaIndex[name]; ok {
		return &p.OutcomeSchemas[idx], true
	}
	return nil, false
}

// StrategyByName returns the named strategy declaration.
func (p *Program) StrategyByName(name string) (*Strategy, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.strategyIndex[name]; ok {
		return &p.Strategies[idx], true
	}
	return nil, false
}

// WorkerByName returns the named worker declaration.
func (p *Program) WorkerByName(name string) (*Worker, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.workerIndex[name]; ok {
		return &p.Workers[idx], true
	}
	return nil, false
}

// RuleByName returns the named rule declaration.
func (p *Program) RuleByName(name string) (*Rule, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.ruleIndex[name]; ok {
		return &p.Rules[idx], true
	}
	return nil, false
}

// FlagByName returns the named flag declaration.
func (p *Program) FlagByName(name string) (*Flag, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.flagIndex[name]; ok {
		return &p.Flags[idx], true
	}
	return nil, false
}

// ExpertRuleByName returns the named expert rule declaration.
func (p *Program) ExpertRuleByName(name string) (*ExpertRule, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.expertIndex[name]; ok {
		return &p.Expert[idx], true
	}
	return nil, false
}

// ArbiterByName returns the named arbiter declaration.
func (p *Program) ArbiterByName(name string) (*Arbiter, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.arbiterIndex[name]; ok {
		return &p.Arbiters[idx], true
	}
	return nil, false
}

// TableByName returns the named table declaration.
func (p *Program) TableByName(name string) (*Table, bool) {
	if p == nil || name == "" {
		return nil, false
	}
	if idx, ok := p.tableIndex[name]; ok {
		return &p.Tables[idx], true
	}
	return nil, false
}

// RebuildIndexes refreshes the internal declaration lookup tables.
func (p *Program) RebuildIndexes() {
	if p == nil {
		return
	}
	p.rebuildIndexes()
}

func (p *Program) rebuildIndexes() {
	p.tagIndex = make(map[string]int, len(p.Tags))
	for i := range p.Tags {
		if _, ok := p.tagIndex[p.Tags[i].Name]; ok {
			continue
		}
		p.tagIndex[p.Tags[i].Name] = i
	}
	p.constIndex = make(map[string]int, len(p.Consts))
	for i := range p.Consts {
		p.constIndex[p.Consts[i].Name] = i
	}
	p.factSchemaIndex = make(map[string]int, len(p.FactSchemas))
	for i := range p.FactSchemas {
		p.factSchemaIndex[p.FactSchemas[i].Name] = i
	}
	p.outcomeSchemaIndex = make(map[string]int, len(p.OutcomeSchemas))
	for i := range p.OutcomeSchemas {
		p.outcomeSchemaIndex[p.OutcomeSchemas[i].Name] = i
	}
	p.strategyIndex = make(map[string]int, len(p.Strategies))
	for i := range p.Strategies {
		p.strategyIndex[p.Strategies[i].Name] = i
	}
	p.workerIndex = make(map[string]int, len(p.Workers))
	for i := range p.Workers {
		p.workerIndex[p.Workers[i].Name] = i
	}
	p.segmentIndex = make(map[string]int, len(p.Segments))
	for i := range p.Segments {
		p.segmentIndex[p.Segments[i].Name] = i
	}
	p.ruleIndex = make(map[string]int, len(p.Rules))
	for i := range p.Rules {
		p.ruleIndex[p.Rules[i].Name] = i
	}
	p.flagIndex = make(map[string]int, len(p.Flags))
	for i := range p.Flags {
		p.flagIndex[p.Flags[i].Name] = i
	}
	p.expertIndex = make(map[string]int, len(p.Expert))
	for i := range p.Expert {
		p.expertIndex[p.Expert[i].Name] = i
	}
	p.arbiterIndex = make(map[string]int, len(p.Arbiters))
	for i := range p.Arbiters {
		p.arbiterIndex[p.Arbiters[i].Name] = i
	}
	p.tableIndex = make(map[string]int, len(p.Tables))
	for i := range p.Tables {
		p.tableIndex[p.Tables[i].Name] = i
	}
}
