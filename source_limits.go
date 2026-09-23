package arbiter

import "fmt"

// MaxSourceBytes bounds the size of .arb source text accepted before it
// reaches the gotreesitter parser (see parseTreeWithLanguage, the single
// chokepoint used by ParseSource, Compile, and include expansion). An
// oversized file is rejected immediately instead of spending parse time on
// it. The default is generous; a server that compiles untrusted input (the
// gRPC PublishBundle path, the LSP, the WASM playground) can lower it.
//
// This is a package-level variable rather than a per-call option so every
// entry point is protected uniformly without changing their signatures; set
// it once at startup, before any concurrent Compile/ParseSource calls.
var MaxSourceBytes = 8 << 20 // 8 MiB

// MaxSourceNestingDepth bounds the depth of unclosed `(`, `{`, `[` brackets
// in .arb source text — checked before parsing, ignoring bracket-like bytes
// inside string literals and comments. gotreesitter's GLR parser has a
// known slowdown on deeply nested parens (tracked upstream, not addressed
// in this repo); rejecting pathological nesting here means hostile input
// never reaches that slow path instead of arbiter trying to survive it. The
// default is far above anything a hand-written or generated rule needs —
// real .arb files nest a handful of levels deep at most.
//
// See MaxSourceBytes for why this is a package-level variable.
var MaxSourceNestingDepth = 512

// checkSourceLimits rejects source text that exceeds MaxSourceBytes or
// MaxSourceNestingDepth before it reaches the parser.
func checkSourceLimits(source []byte) error {
	if MaxSourceBytes > 0 && len(source) > MaxSourceBytes {
		return fmt.Errorf("source is %d bytes, exceeds the %d-byte limit (MaxSourceBytes)", len(source), MaxSourceBytes)
	}
	if MaxSourceNestingDepth > 0 {
		if depth := sourceNestingDepth(source); depth > MaxSourceNestingDepth {
			return fmt.Errorf("source nests %d levels of ( { [ deep, exceeds the %d-level limit (MaxSourceNestingDepth)", depth, MaxSourceNestingDepth)
		}
	}
	return nil
}

// sourceNestingDepth returns the maximum depth of unclosed `(`, `{`, `[`
// bytes in source, skipping over string literals and comments so bracket
// characters inside them don't count. It is a best-effort lexical scan, not
// a full parse: it matches the grammar's own comment and string-literal
// token patterns (see grammar/dsl/grammar.go's "comment" and
// "string_literal" definitions — `#...`/`//...` to end of line, `/* ... */`
// blocks with no nesting, and `"..."` strings with no escape sequences).
// The one known imprecision is `#` inside an arbiter target clause
// (`slack #channel`), which this scan treats as a line comment like any
// other `#`; that only makes the scan more conservative (it skips over,
// rather than double-counts, that text), and slack targets are not an
// expression-nesting vector.
//
// Byte-wise scanning is safe for non-ASCII source: UTF-8 continuation and
// multi-byte lead bytes are always >= 0x80, so they can never be mistaken
// for the ASCII structural bytes this function looks for.
func sourceNestingDepth(source []byte) int {
	depth, maxDepth := 0, 0
	n := len(source)
	for i := 0; i < n; i++ {
		switch source[i] {
		case '(', '{', '[':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
		case ')', '}', ']':
			if depth > 0 {
				depth--
			}
		case '"':
			// String literal: "[^"]*" — no escapes, so the next quote (or
			// EOF) always ends it.
			i++
			for i < n && source[i] != '"' {
				i++
			}
		case '#':
			i = skipToLineEnd(source, i)
		case '/':
			if i+1 < n && source[i+1] == '/' {
				i = skipToLineEnd(source, i)
			} else if i+1 < n && source[i+1] == '*' {
				if end := indexBlockCommentEnd(source, i+2); end >= 0 {
					i = end + 1 // land on the trailing '/'
				} else {
					i = n // unterminated block comment: rest of file is a comment
				}
			}
		}
	}
	return maxDepth
}

func skipToLineEnd(source []byte, i int) int {
	for i < len(source) && source[i] != '\n' {
		i++
	}
	return i
}

// indexBlockCommentEnd returns the index of the '*' that starts the closing
// "*/" for a block comment whose body starts at i, or -1 if unterminated.
func indexBlockCommentEnd(source []byte, i int) int {
	for ; i+1 < len(source); i++ {
		if source[i] == '*' && source[i+1] == '/' {
			return i
		}
	}
	return -1
}
