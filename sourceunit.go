package arbiter

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/internal/parseutil"
	"github.com/odvcencio/arbiter/ir"
	gotreesitter "github.com/odvcencio/gotreesitter"
)

// SourceUnit is one fully expanded .arb compilation unit loaded from disk.
type SourceUnit struct {
	Source  []byte
	Files   []string
	Origins []SourceOrigin
}

// SourceOrigin maps one generated declaration back to its source file and line.
type SourceOrigin struct {
	GeneratedLine int
	File          string
	SourceLine    int
	Kind          string
	Name          string
}

// DiagnosticError is one user-facing diagnostic with file and position data.
type DiagnosticError struct {
	File    string
	Line    int
	Column  int
	Message string
	Err     error
}

func (e *DiagnosticError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.File == "" {
		return e.Message
	}
	if e.Line <= 0 {
		return fmt.Sprintf("%s: %s", e.File, e.Message)
	}
	if e.Column <= 0 {
		return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Message)
	}
	return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Column, e.Message)
}

func (e *DiagnosticError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type positionedError struct {
	Line    int
	Column  int
	Message string
	Err     error
}

func (e *positionedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Line <= 0 {
		return e.Message
	}
	if e.Column <= 0 {
		return fmt.Sprintf("%d: %s", e.Line, e.Message)
	}
	return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Message)
}

func (e *positionedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ParsedSource is one parsed in-memory .arb source ready for compilation reuse.
type ParsedSource struct {
	Source []byte
	Lang   *gotreesitter.Language
	Root   *gotreesitter.Node
}

// OriginForLine returns the declaration origin that produced a generated line.
func (u *SourceUnit) OriginForLine(line int) (SourceOrigin, bool) {
	if u == nil || line <= 0 || len(u.Origins) == 0 {
		return SourceOrigin{}, false
	}
	best := u.Origins[0]
	found := false
	for _, origin := range u.Origins {
		if origin.GeneratedLine > line {
			break
		}
		best = origin
		found = true
	}
	return best, found
}

func (u *SourceUnit) originForName(name string, kinds ...string) (SourceOrigin, bool) {
	if u == nil || name == "" {
		return SourceOrigin{}, false
	}
	for _, origin := range u.Origins {
		if origin.Name != name {
			continue
		}
		for _, kind := range kinds {
			if origin.Kind == kind {
				return origin, true
			}
		}
	}
	return SourceOrigin{}, false
}

// IsDiagnosticError reports whether err contains a file-positioned diagnostic.
func IsDiagnosticError(err error) bool {
	var diag *DiagnosticError
	return errors.As(err, &diag)
}

// WrapFileError remaps a generated-source error back to the original included file.
func WrapFileError(unit *SourceUnit, err error) error {
	if unit == nil || err == nil {
		return err
	}
	var diag *DiagnosticError
	if errors.As(err, &diag) {
		return err
	}
	var pos *positionedError
	if errors.As(err, &pos) {
		if mapped, ok := unit.mapPosition(pos.Line, pos.Column, pos.Message, err); ok {
			return mapped
		}
	}
	if mapped, ok := unit.mapNamedError(err); ok {
		return mapped
	}
	return err
}

// LoadFileUnit reads a root .arb file, resolves top-level include statements,
// and returns the merged compilation unit.
func LoadFileUnit(path string) (*SourceUnit, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}

	lang, err := GetLanguage()
	if err != nil {
		return nil, fmt.Errorf("get language: %w", err)
	}

	loader := &sourceUnitLoader{
		lang:     lang,
		seen:     make(map[string]struct{}),
		decls:    make(map[string]SourceOrigin),
		stackPos: make(map[string]int),
	}
	source, err := loader.expand(absPath)
	if err != nil {
		return nil, err
	}
	return &SourceUnit{
		Source:  source,
		Files:   append([]string(nil), loader.files...),
		Origins: append([]SourceOrigin(nil), loader.origins...),
	}, nil
}

// LoadFileSource is a convenience wrapper that returns only the expanded source.
func LoadFileSource(path string) ([]byte, error) {
	unit, err := LoadFileUnit(path)
	if err != nil {
		return nil, err
	}
	return unit.Source, nil
}

// LoadFileParsed resolves includes and parses a file-backed .arb program once.
func LoadFileParsed(path string) (*SourceUnit, *ParsedSource, error) {
	unit, err := LoadFileUnit(path)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := ParseSource(unit.Source)
	if err != nil {
		return nil, nil, WrapFileError(unit, err)
	}
	return unit, parsed, nil
}

// ParseSource parses raw .arb source for reuse across multiple compilation steps.
func ParseSource(source []byte) (*ParsedSource, error) {
	lang, root, err := parseTree(source)
	if err != nil {
		return nil, err
	}
	if err := rejectIncludeDeclarations(root, source, lang); err != nil {
		return nil, err
	}
	return &ParsedSource{
		Source: append([]byte(nil), source...),
		Lang:   lang,
		Root:   root,
	}, nil
}

// CompileParsed compiles a previously parsed source into a ruleset.
func CompileParsed(parsed *ParsedSource) (*compiler.CompiledRuleset, error) {
	if parsed == nil {
		return nil, fmt.Errorf("nil parsed source")
	}
	program, err := ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
	if err != nil {
		return nil, err
	}
	if err := validateProgram(program); err != nil {
		return nil, err
	}
	return compiler.CompileIR(program)
}

// CompileFullParsed compiles a previously parsed source and extracts segments
// plus arbiter declarations.
func CompileFullParsed(parsed *ParsedSource) (*CompileResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("nil parsed source")
	}
	program, err := ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
	if err != nil {
		return nil, err
	}
	if err := validateProgram(program); err != nil {
		return nil, err
	}
	rs, err := compiler.CompileIR(program)
	if err != nil {
		return nil, err
	}
	segs, err := compileSegments(program)
	if err != nil {
		return nil, err
	}
	arbiters, err := compileArbiters(program)
	if err != nil {
		return nil, err
	}
	return &CompileResult{
		Ruleset:  rs,
		Segments: segs,
		Arbiters: arbiters,
		Program:  program,
	}, nil
}

// CompileFile compiles a file-backed .arb program with include resolution.
func CompileFile(path string) (*compiler.CompiledRuleset, error) {
	unit, parsed, err := LoadFileParsed(path)
	if err != nil {
		return nil, err
	}
	rs, err := CompileParsed(parsed)
	if err != nil {
		return nil, WrapFileError(unit, err)
	}
	return rs, nil
}

// CompileFullFile compiles a file-backed .arb program with include resolution.
func CompileFullFile(path string) (*CompileResult, error) {
	unit, parsed, err := LoadFileParsed(path)
	if err != nil {
		return nil, err
	}
	full, err := CompileFullParsed(parsed)
	if err != nil {
		return nil, WrapFileError(unit, err)
	}
	return full, nil
}

type sourceUnitLoader struct {
	lang     *gotreesitter.Language
	files    []string
	origins  []SourceOrigin
	stack    []string
	seen     map[string]struct{}
	decls    map[string]SourceOrigin
	stackPos map[string]int
}

func (l *sourceUnitLoader) expand(path string) ([]byte, error) {
	var out strings.Builder
	generatedLine := 1
	if err := l.expandInto(path, &out, &generatedLine); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

func (l *sourceUnitLoader) expandInto(path string, out *strings.Builder, generatedLine *int) error {
	if _, ok := l.seen[path]; ok {
		return nil
	}
	if idx, ok := l.stackPos[path]; ok {
		cycle := append(append([]string(nil), l.stack[idx:]...), path)
		return fmt.Errorf("include cycle: %s", strings.Join(cycle, " -> "))
	}

	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	root, err := parseTreeWithLanguage(source, l.lang)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	l.stackPos[path] = len(l.stack)
	l.stack = append(l.stack, path)
	l.files = append(l.files, path)
	defer func() {
		delete(l.stackPos, path)
		l.stack = l.stack[:len(l.stack)-1]
	}()

	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type(l.lang) == "include_declaration" {
			pathNode := child.ChildByFieldName("path", l.lang)
			if pathNode == nil {
				return fmt.Errorf("%s: include missing path", path)
			}
			includePath := parseutil.StripQuotes(nodeText(pathNode, source))
			if includePath == "" {
				return fmt.Errorf("%s: include path is empty", path)
			}
			resolved := includePath
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(path), includePath)
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return fmt.Errorf("%s: resolve include %s: %w", path, includePath, err)
			}
			if err := l.expandInto(filepath.Clean(resolved), out, generatedLine); err != nil {
				return err
			}
			continue
		}
		origin := declarationOrigin(child, source, path, *generatedLine, l.lang)
		if key, ok := declarationKey(origin); ok {
			if first, exists := l.decls[key]; exists {
				return fmt.Errorf("duplicate %s %q: %s:%d and %s:%d", origin.Kind, origin.Name, first.File, first.SourceLine, origin.File, origin.SourceLine)
			}
			l.decls[key] = origin
		}
		l.origins = append(l.origins, origin)
		out.WriteString(nodeText(child, source))
		out.WriteByte('\n')
		*generatedLine += declarationLineCount(child, source)
	}

	l.seen[path] = struct{}{}
	return nil
}

func declarationOrigin(node *gotreesitter.Node, source []byte, path string, generatedLine int, lang *gotreesitter.Language) SourceOrigin {
	origin := SourceOrigin{
		GeneratedLine: generatedLine,
		File:          path,
		SourceLine:    1 + strings.Count(string(source[:node.StartByte()]), "\n"),
		Kind:          node.Type(lang),
	}
	if nameNode := node.ChildByFieldName("name", lang); nameNode != nil {
		origin.Name = parseutil.StripQuotes(nodeText(nameNode, source))
	}
	return origin
}

func declarationKey(origin SourceOrigin) (string, bool) {
	switch origin.Kind {
	case "const_declaration", "segment_declaration", "rule_declaration", "expert_rule_declaration", "flag_declaration", "feature_declaration", "fact_declaration", "outcome_declaration", "arbiter_declaration":
		if origin.Name == "" {
			return "", false
		}
		return origin.Kind + ":" + origin.Name, true
	default:
		return "", false
	}
}

func declarationLineCount(node *gotreesitter.Node, source []byte) int {
	return strings.Count(nodeText(node, source), "\n") + 1
}

func parseTree(source []byte) (*gotreesitter.Language, *gotreesitter.Node, error) {
	lang, err := GetLanguage()
	if err != nil {
		return nil, nil, fmt.Errorf("get language: %w", err)
	}
	root, err := parseTreeWithLanguage(source, lang)
	if err != nil {
		return nil, nil, err
	}
	return lang, root, nil
}

func parseTreeWithLanguage(source []byte, lang *gotreesitter.Language) (*gotreesitter.Node, error) {
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	root := tree.RootNode()
	if root.HasError() {
		return nil, buildParseError(root, source, lang)
	}
	return root, nil
}

func rejectIncludeDeclarations(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) error {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type(lang) != "include_declaration" {
			continue
		}
		pathNode := child.ChildByFieldName("path", lang)
		if pathNode == nil {
			return fmt.Errorf("include declarations require file-based compilation; use CompileFile or LoadFileUnit")
		}
		return fmt.Errorf("include %s requires file-based compilation; use CompileFile or LoadFileUnit", nodeText(pathNode, source))
	}
	return nil
}

func buildParseError(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) error {
	node := firstParseProblem(root)
	if node == nil {
		return &positionedError{
			Line:    1,
			Column:  1,
			Message: "parse error",
			Err:     fmt.Errorf("parse errors in arbiter source"),
		}
	}
	point := node.StartPoint()
	return &positionedError{
		Line:    int(point.Row) + 1,
		Column:  int(point.Column) + 1,
		Message: parseProblemMessage(node, source, lang),
		Err:     fmt.Errorf("parse errors in arbiter source"),
	}
}

func firstParseProblem(node *gotreesitter.Node) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	if node.IsMissing() || node.IsError() {
		return node
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		if child := node.Child(i); child != nil {
			if found := firstParseProblem(child); found != nil {
				return found
			}
		}
	}
	return nil
}

func parseProblemMessage(node *gotreesitter.Node, source []byte, lang *gotreesitter.Language) string {
	if node == nil {
		return "parse error"
	}
	kind := node.Type(lang)
	if node.IsMissing() {
		if kind != "" && kind != "ERROR" {
			return fmt.Sprintf("parse error: missing %s", kind)
		}
		return "parse error: missing token"
	}
	snippet := strings.TrimSpace(nodeText(node, source))
	snippet = strings.Join(strings.Fields(snippet), " ")
	if len(snippet) > 40 {
		snippet = snippet[:37] + "..."
	}
	if snippet != "" {
		return fmt.Sprintf("parse error near %q", snippet)
	}
	return "parse error"
}

func (u *SourceUnit) mapPosition(line, column int, message string, err error) (*DiagnosticError, bool) {
	if line <= 0 {
		return nil, false
	}
	origin, ok := u.OriginForLine(line)
	if !ok {
		return nil, false
	}
	mappedLine := origin.SourceLine + (line - origin.GeneratedLine)
	return &DiagnosticError{
		File:    origin.File,
		Line:    mappedLine,
		Column:  column,
		Message: message,
		Err:     err,
	}, true
}

func (u *SourceUnit) mapNamedError(err error) (*DiagnosticError, bool) {
	message := err.Error()
	if diag, ok := u.namedDiagnostic(message, err, "rule ", "rule_declaration", "expert_rule_declaration"); ok {
		return diag, true
	}
	if diag, ok := u.namedDiagnostic(message, err, "arbiter ", "arbiter_declaration"); ok {
		return diag, true
	}
	if diag, ok := u.namedDiagnostic(message, err, "expert rule ", "expert_rule_declaration"); ok {
		return diag, true
	}
	if diag, ok := u.namedDiagnostic(message, err, "flag ", "flag_declaration"); ok {
		return diag, true
	}
	if diag, ok := u.namedDiagnostic(message, err, "compile segment ", "segment_declaration"); ok {
		return diag, true
	}
	return nil, false
}

func unitDiagnostic(origin SourceOrigin, message string, err error) *DiagnosticError {
	return &DiagnosticError{
		File:    origin.File,
		Line:    origin.SourceLine,
		Column:  1,
		Message: message,
		Err:     err,
	}
}

func (u *SourceUnit) namedDiagnostic(message string, err error, prefix string, kinds ...string) (*DiagnosticError, bool) {
	if !strings.HasPrefix(message, prefix) {
		return nil, false
	}
	rest := strings.TrimPrefix(message, prefix)
	name, tail, ok := splitDiagnosticName(rest)
	if !ok {
		return nil, false
	}
	origin, ok := u.originForName(name, kinds...)
	if !ok {
		return nil, false
	}
	tail = strings.TrimSpace(tail)
	if tail == "" {
		tail = prefix + name
	}
	diag := unitDiagnostic(origin, message, err)
	if line, col, embedded, ok := parseEmbeddedPosition(tail); ok {
		diag.Line = origin.SourceLine + maxInt(0, line-1)
		diag.Column = col
		diag.Message = embedded
		return diag, true
	}
	return diag, true
}

func splitDiagnosticName(rest string) (string, string, bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", false
	}
	if rest[0] == '"' {
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return "", "", false
		}
		name := rest[1 : end+1]
		return name, strings.TrimSpace(rest[end+2:]), name != ""
	}
	for i, r := range rest {
		if r == ' ' || r == ':' {
			name := strings.TrimSpace(rest[:i])
			return name, strings.TrimSpace(rest[i:]), name != ""
		}
	}
	return strings.TrimSpace(rest), "", true
}

func parseEmbeddedPosition(message string) (int, int, string, bool) {
	first, rest, ok := strings.Cut(message, ":")
	if !ok {
		return 0, 0, "", false
	}
	line, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil || line <= 0 {
		return 0, 0, "", false
	}
	second, tail, ok := strings.Cut(rest, ":")
	if !ok {
		return line, 0, strings.TrimSpace(rest), true
	}
	if col, err := strconv.Atoi(strings.TrimSpace(second)); err == nil && col > 0 {
		return line, col, strings.TrimSpace(tail), true
	}
	return line, 0, strings.TrimSpace(rest), true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
