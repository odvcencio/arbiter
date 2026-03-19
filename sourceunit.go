package arbiter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/internal/parseutil"
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
	return compiler.CompileCST(parsed.Root, parsed.Source, parsed.Lang)
}

// CompileFullParsed compiles a previously parsed source and extracts segments.
func CompileFullParsed(parsed *ParsedSource) (*CompileResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("nil parsed source")
	}
	rs, err := compiler.CompileCST(parsed.Root, parsed.Source, parsed.Lang)
	if err != nil {
		return nil, err
	}
	segs, err := compileSegments(parsed.Root, parsed.Source, parsed.Lang)
	if err != nil {
		return nil, err
	}
	return &CompileResult{
		Ruleset:  rs,
		Segments: segs,
	}, nil
}

// CompileFile compiles a file-backed .arb program with include resolution.
func CompileFile(path string) (*compiler.CompiledRuleset, error) {
	source, err := LoadFileSource(path)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseSource(source)
	if err != nil {
		return nil, err
	}
	return CompileParsed(parsed)
}

// CompileFullFile compiles a file-backed .arb program with include resolution.
func CompileFullFile(path string) (*CompileResult, error) {
	source, err := LoadFileSource(path)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseSource(source)
	if err != nil {
		return nil, err
	}
	return CompileFullParsed(parsed)
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
	case "const_declaration", "segment_declaration", "rule_declaration", "expert_rule_declaration", "flag_declaration", "feature_declaration":
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
		return nil, fmt.Errorf("parse errors in arbiter source")
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
