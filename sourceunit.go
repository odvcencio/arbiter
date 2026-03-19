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
	Source []byte
	Files  []string
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
		stackPos: make(map[string]int),
	}
	source, err := loader.expand(absPath)
	if err != nil {
		return nil, err
	}
	return &SourceUnit{
		Source: source,
		Files:  append([]string(nil), loader.files...),
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

// CompileFile compiles a file-backed .arb program with include resolution.
func CompileFile(path string) (*compiler.CompiledRuleset, error) {
	source, err := LoadFileSource(path)
	if err != nil {
		return nil, err
	}
	return Compile(source)
}

// CompileFullFile compiles a file-backed .arb program with include resolution.
func CompileFullFile(path string) (*CompileResult, error) {
	source, err := LoadFileSource(path)
	if err != nil {
		return nil, err
	}
	return CompileFull(source)
}

type sourceUnitLoader struct {
	lang     *gotreesitter.Language
	files    []string
	stack    []string
	seen     map[string]struct{}
	stackPos map[string]int
}

func (l *sourceUnitLoader) expand(path string) ([]byte, error) {
	if _, ok := l.seen[path]; ok {
		return nil, nil
	}
	if idx, ok := l.stackPos[path]; ok {
		cycle := append(append([]string(nil), l.stack[idx:]...), path)
		return nil, fmt.Errorf("include cycle: %s", strings.Join(cycle, " -> "))
	}

	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	root, err := parseTreeWithLanguage(source, l.lang)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	l.stackPos[path] = len(l.stack)
	l.stack = append(l.stack, path)
	l.files = append(l.files, path)
	defer func() {
		delete(l.stackPos, path)
		l.stack = l.stack[:len(l.stack)-1]
	}()

	var out strings.Builder
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type(l.lang) == "include_declaration" {
			pathNode := child.ChildByFieldName("path", l.lang)
			if pathNode == nil {
				return nil, fmt.Errorf("%s: include missing path", path)
			}
			includePath := parseutil.StripQuotes(nodeText(pathNode, source))
			if includePath == "" {
				return nil, fmt.Errorf("%s: include path is empty", path)
			}
			resolved := includePath
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(path), includePath)
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return nil, fmt.Errorf("%s: resolve include %s: %w", path, includePath, err)
			}
			expanded, err := l.expand(filepath.Clean(resolved))
			if err != nil {
				return nil, err
			}
			if len(expanded) > 0 {
				out.Write(expanded)
				if expanded[len(expanded)-1] != '\n' {
					out.WriteByte('\n')
				}
			}
			continue
		}
		out.WriteString(nodeText(child, source))
		out.WriteByte('\n')
	}

	l.seen[path] = struct{}{}
	return []byte(out.String()), nil
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
