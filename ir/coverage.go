package ir

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// CoverageKind says which source check produced a SourceCoverageError.
type CoverageKind int

const (
	// CoverageUnparsed is source text that no parsed token covers.
	CoverageUnparsed CoverageKind = iota
	// CoveragePercent is a "%" the grammar did not consume as remainder.
	CoveragePercent
)

// SourceCoverageError reports source the parser did not turn into syntax.
// Error recovery can drop a whole declaration and leave loose tokens or
// nothing at all under the root, without setting HasError.
type SourceCoverageError struct {
	Kind CoverageKind
	// Offset is the byte offset of the first uncovered byte.
	Offset int
	// Line and Col are 1-based. Col counts bytes.
	Line, Col int
	// Snippet is a short excerpt of the source starting at Offset.
	Snippet string
	// InDeclaration is true when the text sits inside, or is a loose token of,
	// a declaration that was dropped.
	InDeclaration bool
}

// Message is the error text without the position, for callers that add their
// own file and position prefix.
func (e *SourceCoverageError) Message() string {
	if e.Kind == CoveragePercent {
		return fmt.Sprintf("invalid or incomplete arithmetic expression near %q: use pct for percentage quantities", e.Snippet)
	}
	return fmt.Sprintf("unparsed source near %q: %s", e.Snippet, e.consequence())
}

func (e *SourceCoverageError) consequence() string {
	if e.InDeclaration {
		return "the declaration was not compiled"
	}
	return "the skipped text was not compiled"
}

func (e *SourceCoverageError) Error() string {
	if e.Kind == CoveragePercent {
		return fmt.Sprintf("invalid or incomplete arithmetic expression at %d:%d near %q: use pct for percentage quantities", e.Line, e.Col, e.Snippet)
	}
	return fmt.Sprintf("unparsed source at %d:%d near %q: %s", e.Line, e.Col, e.Snippet, e.consequence())
}

// rootNodeKinds are the node types that may appear directly under source_file.
var rootNodeKinds = map[string]struct{}{
	"comment":                 {},
	"include_declaration":     {},
	"import_declaration":      {},
	"tag_declaration":         {},
	"input_declaration":       {},
	"feature_declaration":     {},
	"fact_declaration":        {},
	"outcome_declaration":     {},
	"strategy_declaration":    {},
	"worker_declaration":      {},
	"const_declaration":       {},
	"template_declaration":    {},
	"arbiter_declaration":     {},
	"rule_declaration":        {},
	"expert_rule_declaration": {},
	"segment_declaration":     {},
	"flag_declaration":        {},
	"table_declaration":       {},
}

// CheckSourceCoverage fails when part of source did not become syntax. The
// parser can discard text, or a whole declaration, without setting HasError.
// It rejects:
//
//   - a root child that is not a declaration or a comment, such as the loose
//     tokens left behind when recovery drops a declaration;
//   - any byte that no parsed token covers, other than whitespace
//     ([ \t\r\n], the grammar's set). Comments, strings and other literals
//     are tokens, so their contents are covered.
//
// Pass the original source bytes of the file. The error is a
// *SourceCoverageError with a line, column and snippet.
func CheckSourceCoverage(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) error {
	if root == nil {
		return fmt.Errorf("nil root node")
	}
	if lang == nil {
		return fmt.Errorf("nil language")
	}
	return checkSource(root, source, lang, false)
}

func isGrammarSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

func newCoverageError(source []byte, pos int, kind CoverageKind, inDecl bool) *SourceCoverageError {
	bom := 0
	if bytes.HasPrefix(source, []byte{0xEF, 0xBB, 0xBF}) {
		bom = 3
	}
	line := 1 + bytes.Count(source[:pos], []byte("\n"))
	lineStart := bytes.LastIndexByte(source[:pos], '\n') + 1
	if line == 1 {
		lineStart = bom
	}
	col := pos - lineStart + 1
	snippet := source[pos:]
	if i := bytes.IndexByte(snippet, '\n'); i >= 0 {
		snippet = snippet[:i]
	}
	const maxSnippet = 24
	suffix := ""
	if len(snippet) > maxSnippet {
		snippet = snippet[:maxSnippet]
		for len(snippet) > 0 && !utf8.Valid(snippet) {
			snippet = snippet[:len(snippet)-1]
		}
		suffix = "..."
	}
	return &SourceCoverageError{
		Kind:          kind,
		Offset:        pos,
		Line:          line,
		Col:           col,
		Snippet:       strings.TrimRight(string(snippet), " \t\r") + suffix,
		InDeclaration: inDecl,
	}
}

type coverageWalker struct {
	source []byte
	lang   *gotreesitter.Language
	arith  bool
	cursor uint32
	stack  []*gotreesitter.Node
	// decls are the [start,end) spans of root children, in order.
	decls [][2]uint32
	next  int
	// looseStart is the start of the first root child that is not a
	// declaration or comment, or -1. Such tokens are what recovery leaves
	// behind when it drops a declaration.
	looseStart int
}

func checkSource(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language, arith bool) error {
	w := &coverageWalker{source: source, lang: lang, arith: arith, looseStart: -1}
	if bytes.HasPrefix(source, []byte{0xEF, 0xBB, 0xBF}) {
		w.cursor = 3
	}
	if root.Type(lang) == "source_file" {
		for i := 0; i < int(root.ChildCount()); i++ {
			c := root.Child(i)
			if _, ok := rootNodeKinds[c.Type(lang)]; !ok {
				if w.looseStart < 0 {
					w.looseStart = int(c.StartByte())
				}
				continue
			}
			if c.Type(lang) != "comment" {
				w.decls = append(w.decls, [2]uint32{c.StartByte(), c.EndByte()})
			}
		}
	}
	if err := w.walk(root, nil); err != nil {
		return err
	}
	if err := w.gap(uint32(len(source))); err != nil {
		return err
	}
	if w.looseStart >= 0 {
		// Every byte is covered, but only by loose tokens: the declaration
		// they belong to was dropped (for example a missing final brace).
		return newCoverageError(source, w.looseStart, CoverageUnparsed, true)
	}
	return nil
}

// inDeclaration reports whether pos lies inside a root declaration's span.
func (w *coverageWalker) inDeclaration(pos uint32) bool {
	for w.next < len(w.decls) && w.decls[w.next][1] <= pos {
		w.next++
	}
	return w.next < len(w.decls) && w.decls[w.next][0] <= pos
}

// hiddenOperator reports whether text is a comparison operator that the
// grammar defines as a hidden token (_ge_op, _le_op, _ne_op), so it is real
// source but not a child node. It must sit between the operands of an
// enclosing comparison_expr.
func (w *coverageWalker) hiddenOperator(text []byte, start, end uint32) bool {
	switch string(text) {
	case ">=", "<=", "!=":
	default:
		return false
	}
	for i := len(w.stack) - 1; i >= 0; i-- {
		c := w.stack[i]
		if c.Type(w.lang) != "comparison_expr" {
			continue
		}
		left := c.ChildByFieldName("left", w.lang)
		right := c.ChildByFieldName("right", w.lang)
		if left != nil && right != nil && left.EndByte() <= start && end <= right.StartByte() {
			return true
		}
	}
	return false
}

// gap checks the bytes from the cursor up to end, which no token covers.
func (w *coverageWalker) gap(end uint32) error {
	start := w.cursor
	if end > uint32(len(w.source)) {
		end = uint32(len(w.source))
	}
	if end <= start {
		return nil
	}
	gap := w.source[start:end]
	first := 0
	for first < len(gap) && isGrammarSpace(gap[first]) {
		first++
	}
	if first == len(gap) {
		return nil
	}
	last := len(gap)
	for last > first && isGrammarSpace(gap[last-1]) {
		last--
	}
	if w.hiddenOperator(gap[first:last], start, end) {
		return nil
	}
	pos := int(start) + first
	kind := CoverageUnparsed
	if gap[first] == '%' {
		kind = CoveragePercent
	}
	inDecl := w.inDeclaration(uint32(pos)) || (w.looseStart >= 0 && pos >= w.looseStart)
	return newCoverageError(w.source, pos, kind, inDecl)
}

func (w *coverageWalker) walk(n, parent *gotreesitter.Node) error {
	kind := n.Type(w.lang)
	numericLiteral := kind == "number_literal" || kind == "decimal_literal" || kind == "quantity_literal"
	if w.arith && numericLiteral {
		// Signed literals contain a visible sign and a hidden immediate
		// token. Recovery can retain only the sign, without HasError.
		// Check the whole literal before treating its span as opaque.
		text := string(w.source[n.StartByte():n.EndByte()])
		if kind != "number_literal" {
			parts := strings.Fields(text)
			if len(parts) != 2 {
				return fmt.Errorf("invalid %s", kind)
			}
			text = parts[0]
		}
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return fmt.Errorf("invalid %s: %w", kind, err)
		}
	}
	if n.ChildCount() == 0 || numericLiteral {
		if err := w.gap(n.StartByte()); err != nil {
			return err
		}
		if w.arith {
			if err := w.checkOperatorLeaf(n, parent); err != nil {
				return err
			}
		}
		if end := n.EndByte(); end > w.cursor {
			w.cursor = end
		}
		return nil
	}
	w.stack = append(w.stack, n)
	defer func() { w.stack = w.stack[:len(w.stack)-1] }()
	for i := 0; i < int(n.ChildCount()); i++ {
		if err := w.walk(n.Child(i), n); err != nil {
			return err
		}
	}
	return nil
}

// checkOperatorLeaf rejects an arithmetic operator that recovery kept outside
// an expression. A minus may also introduce a signed literal, and arbiter
// targets have a wildcard token. Named leaves (strings, paths, comments,
// timestamps and exponents) retain their contents as literal text.
func (w *coverageWalker) checkOperatorLeaf(n, parent *gotreesitter.Node) error {
	source, lang := w.source, w.lang
	if n.IsNamed() || n.EndByte() != n.StartByte()+1 || bytes.IndexByte([]byte("+-*/%"), source[n.StartByte()]) < 0 {
		return nil
	}
	if parent == nil {
		return fmt.Errorf("arithmetic operator outside an expression")
	}
	opChar := source[n.StartByte()]
	kind := parent.Type(lang)
	if opChar == '*' && kind == "arbiter_wildcard" {
		return nil
	}
	if kind != "math_expr" && kind != "product_expr" {
		return fmt.Errorf("invalid or incomplete arithmetic expression; use (0 - value) to negate an expression")
	}
	op := parent.ChildByFieldName("op", lang)
	left := parent.ChildByFieldName("left", lang)
	right := parent.ChildByFieldName("right", lang)
	if op == nil || op.StartByte() != n.StartByte() || left == nil || right == nil || left.EndByte() == left.StartByte() || right.EndByte() == right.StartByte() {
		return fmt.Errorf("invalid or incomplete arithmetic expression")
	}
	return nil
}
