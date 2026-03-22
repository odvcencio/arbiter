package ir

// ExprID indexes into Program.Exprs.
type ExprID uint32

// Program is the lowered in-process representation of a parsed `.arb` source.
type Program struct {
	Consts   []Const
	Features []Feature
	Segments []Segment
	Rules    []Rule
	Flags    []Flag
	Expert   []ExpertRule
	Arbiters []Arbiter
	Exprs    []Expr

	constIndex   map[string]int
	segmentIndex map[string]int
	ruleIndex    map[string]int
	flagIndex    map[string]int
	expertIndex  map[string]int
	arbiterIndex map[string]int
}

// Span stores byte and point ranges for a declaration or expression.
type Span struct {
	StartByte uint32
	EndByte   uint32
	StartRow  uint32
	StartCol  uint32
	EndRow    uint32
	EndCol    uint32
}

// Const is one top-level const declaration.
type Const struct {
	Name  string
	Span  Span
	Value ExprID
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

// Segment is one top-level segment declaration.
type Segment struct {
	Name      string
	Span      Span
	Condition ExprID
}

// Rule is one top-level standard rule declaration.
type Rule struct {
	Name         string
	Span         Span
	Priority     int32
	KillSwitch   bool
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
	Type       FlagType
	Default    string
	KillSwitch bool
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
	Rollout      *Rollout
	Variant      string
	Split        *FlagSplit
	IsElse       bool
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
	Name            string
	Span            Span
	Priority        int32
	KillSwitch      bool
	Prereqs         []string
	Excludes        []string
	Segment         string
	Lets            []LetBinding
	Condition       ExprID
	HasCondition    bool
	Rollout         *Rollout
	PerFact         bool
	NoLoop          bool
	Stable          bool
	ActivationGroup string
	ActionKind      ExpertActionKind
	Target          string
	Params          []ActionParam
	SetParams       []ActionParam
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
	ExprStringLit ExprKind = "string_lit"
	ExprNumberLit ExprKind = "number_lit"
	ExprBoolLit   ExprKind = "bool_lit"
	ExprNullLit   ExprKind = "null_lit"
	ExprListLit   ExprKind = "list_lit"

	ExprVarRef    ExprKind = "var_ref"
	ExprConstRef  ExprKind = "const_ref"
	ExprLocalRef  ExprKind = "local_ref"
	ExprSecretRef ExprKind = "secret_ref"

	ExprBinary     ExprKind = "binary"
	ExprUnary      ExprKind = "unary"
	ExprBetween    ExprKind = "between"
	ExprQuantifier ExprKind = "quantifier"
	ExprAggregate  ExprKind = "aggregate"
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

// RebuildIndexes refreshes the internal declaration lookup tables.
func (p *Program) RebuildIndexes() {
	if p == nil {
		return
	}
	p.rebuildIndexes()
}

func (p *Program) rebuildIndexes() {
	p.constIndex = make(map[string]int, len(p.Consts))
	for i := range p.Consts {
		p.constIndex[p.Consts[i].Name] = i
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
}
