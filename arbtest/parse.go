package arbtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/ir"
)

// ParseFile parses one .test.arb file.
func ParseFile(path string) (*Suite, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &parser{
		file: filepath.Clean(path),
		src:  string(source),
	}
	return p.parse()
}

type parser struct {
	file string
	src  string
	pos  int
}

func (p *parser) parse() (*Suite, error) {
	suite := &Suite{File: p.file}
	p.skipSpace()
	for !p.eof() {
		switch {
		case p.peekKeyword("test"):
			test, err := p.parseTest()
			if err != nil {
				return nil, err
			}
			suite.Tests = append(suite.Tests, test)
		case p.peekKeyword("scenario"):
			scenario, err := p.parseScenario()
			if err != nil {
				return nil, err
			}
			suite.Scenarios = append(suite.Scenarios, scenario)
		default:
			return nil, p.errorf("expected test or scenario")
		}
		p.skipSpace()
	}
	return suite, nil
}

func (p *parser) parseTest() (TestCase, error) {
	if err := p.consumeKeyword("test"); err != nil {
		return TestCase{}, err
	}
	name, err := p.parseQuotedString()
	if err != nil {
		return TestCase{}, err
	}
	test := TestCase{
		Name:  name,
		Given: make(map[string]any),
	}
	if err := p.consumeChar('{'); err != nil {
		return TestCase{}, err
	}
	for {
		p.skipSpace()
		if p.consumeIf('}') {
			break
		}
		switch {
		case p.peekKeyword("given"):
			given, err := p.parseGivenBlock(true)
			if err != nil {
				return TestCase{}, err
			}
			mergeMap(test.Given, given)
		case p.peekKeyword("expect"):
			expectation, err := p.parseExpectation()
			if err != nil {
				return TestCase{}, err
			}
			test.Expectations = append(test.Expectations, expectation)
		default:
			return TestCase{}, p.errorf("expected given or expect inside test")
		}
	}
	return test, nil
}

func (p *parser) parseScenario() (Scenario, error) {
	if err := p.consumeKeyword("scenario"); err != nil {
		return Scenario{}, err
	}
	name, err := p.parseQuotedString()
	if err != nil {
		return Scenario{}, err
	}
	scenario := Scenario{
		Name:  name,
		Given: make(map[string]any),
	}
	if err := p.consumeChar('{'); err != nil {
		return Scenario{}, err
	}
	for {
		p.skipSpace()
		if p.consumeIf('}') {
			break
		}
		switch {
		case p.peekKeyword("given"):
			given, assertions, err := p.parseScenarioBodyBlock("given")
			if err != nil {
				return Scenario{}, err
			}
			mergeMap(scenario.Given, given)
			scenario.Assertions = append(scenario.Assertions, assertions...)
		case p.peekKeyword("after"):
			step, err := p.parseAfterStabilization()
			if err != nil {
				return Scenario{}, err
			}
			scenario.Steps = append(scenario.Steps, step)
		case p.peekKeyword("at"):
			step, err := p.parseAtStep()
			if err != nil {
				return Scenario{}, err
			}
			scenario.Steps = append(scenario.Steps, step)
		case p.peekKeyword("stream"):
			step, err := p.parseStreamStep()
			if err != nil {
				return Scenario{}, err
			}
			scenario.Steps = append(scenario.Steps, step)
		case p.peekKeyword("within"):
			step, err := p.parseWithinStep()
			if err != nil {
				return Scenario{}, err
			}
			scenario.Steps = append(scenario.Steps, step)
		default:
			return Scenario{}, p.errorf("expected given, at, after stabilization, stream, or within inside scenario")
		}
	}
	return scenario, nil
}

func (p *parser) parseAfterStabilization() (ScenarioStep, error) {
	if err := p.consumeKeyword("after"); err != nil {
		return ScenarioStep{}, err
	}
	if err := p.consumeKeyword("stabilization"); err != nil {
		return ScenarioStep{}, err
	}
	given, assertions, expectations, err := p.parseStepBody()
	if err != nil {
		return ScenarioStep{}, err
	}
	return ScenarioStep{
		Stabilize:    true,
		Given:        given,
		Assertions:   assertions,
		Expectations: expectations,
	}, nil
}

func (p *parser) parseAtStep() (ScenarioStep, error) {
	if err := p.consumeKeyword("at"); err != nil {
		return ScenarioStep{}, err
	}
	p.skipSpace()
	if !p.consumePrefix("T+") {
		return ScenarioStep{}, p.errorf("expected T+<duration>")
	}
	durationToken := p.readUntilWhitespaceOr('{')
	offset, err := parseStepOffset(durationToken)
	if err != nil {
		return ScenarioStep{}, p.errorf("%v", err)
	}
	given, assertions, expectations, err := p.parseStepBody()
	if err != nil {
		return ScenarioStep{}, err
	}
	return ScenarioStep{
		AtOffset:     offset,
		HasAtOffset:  true,
		Given:        given,
		Assertions:   assertions,
		Expectations: expectations,
	}, nil
}

func (p *parser) parseStreamStep() (ScenarioStep, error) {
	if err := p.consumeKeyword("stream"); err != nil {
		return ScenarioStep{}, err
	}
	name, err := p.parseIdentifier()
	if err != nil {
		return ScenarioStep{}, err
	}
	fields, err := p.parseValueMap()
	if err != nil {
		return ScenarioStep{}, err
	}
	return ScenarioStep{
		StreamEvents: []StreamEvent{{
			Name:   name,
			Fields: fields,
		}},
	}, nil
}

func (p *parser) parseWithinStep() (ScenarioStep, error) {
	if err := p.consumeKeyword("within"); err != nil {
		return ScenarioStep{}, err
	}
	durationToken := p.readUntilWhitespaceOr('{')
	window, err := parseStepOffset(durationToken)
	if err != nil {
		return ScenarioStep{}, p.errorf("%v", err)
	}
	given, assertions, expectations, err := p.parseStepBody()
	if err != nil {
		return ScenarioStep{}, err
	}
	return ScenarioStep{
		WithinWindow: window,
		HasWithin:    true,
		Given:        given,
		Assertions:   assertions,
		Expectations: expectations,
	}, nil
}

func (p *parser) parseStepBody() (map[string]any, []FactSpec, []Expectation, error) {
	if err := p.consumeChar('{'); err != nil {
		return nil, nil, nil, err
	}
	given := make(map[string]any)
	assertions := make([]FactSpec, 0)
	expectations := make([]Expectation, 0)
	for {
		p.skipSpace()
		if p.consumeIf('}') {
			break
		}
		switch {
		case p.peekKeyword("assert"):
			fact, err := p.parseFactAssertion()
			if err != nil {
				return nil, nil, nil, err
			}
			assertions = append(assertions, fact)
		case p.peekKeyword("expect"):
			expectation, err := p.parseExpectation()
			if err != nil {
				return nil, nil, nil, err
			}
			expectations = append(expectations, expectation)
		case isIdentStart(p.peek()):
			path, value, err := p.parseGivenEntry()
			if err != nil {
				return nil, nil, nil, err
			}
			setPath(given, path, value)
		default:
			return nil, nil, nil, p.errorf("expected assert, expect, or path assignment inside scenario step")
		}
	}
	return given, assertions, expectations, nil
}

func (p *parser) parseScenarioBodyBlock(keyword string) (map[string]any, []FactSpec, error) {
	if keyword != "" {
		if err := p.consumeKeyword(keyword); err != nil {
			return nil, nil, err
		}
	}
	if err := p.consumeChar('{'); err != nil {
		return nil, nil, err
	}
	given := make(map[string]any)
	assertions := make([]FactSpec, 0)
	for {
		p.skipSpace()
		if p.consumeIf('}') {
			break
		}
		switch {
		case p.peekKeyword("assert"):
			fact, err := p.parseFactAssertion()
			if err != nil {
				return nil, nil, err
			}
			assertions = append(assertions, fact)
		case isIdentStart(p.peek()):
			path, value, err := p.parseGivenEntry()
			if err != nil {
				return nil, nil, err
			}
			setPath(given, path, value)
		default:
			return nil, nil, p.errorf("expected assert or path assignment")
		}
	}
	return given, assertions, nil
}

func (p *parser) parseGivenBlock(requireKeyword bool) (map[string]any, error) {
	if requireKeyword {
		if err := p.consumeKeyword("given"); err != nil {
			return nil, err
		}
	}
	if err := p.consumeChar('{'); err != nil {
		return nil, err
	}
	given := make(map[string]any)
	for {
		p.skipSpace()
		if p.consumeIf('}') {
			break
		}
		path, value, err := p.parseGivenEntry()
		if err != nil {
			return nil, err
		}
		setPath(given, path, value)
	}
	return given, nil
}

func (p *parser) parseGivenEntry() (string, any, error) {
	path, err := p.parsePath()
	if err != nil {
		return "", nil, err
	}
	if err := p.consumeChar(':'); err != nil {
		return "", nil, err
	}
	raw := p.readValue(true, '\n', '}')
	value, err := parseLiteral(raw)
	if err != nil {
		return "", nil, p.errorf("parse literal %q: %v", raw, err)
	}
	return path, value, nil
}

func (p *parser) parseFactAssertion() (FactSpec, error) {
	if err := p.consumeKeyword("assert"); err != nil {
		return FactSpec{}, err
	}
	factType, err := p.parseIdentifier()
	if err != nil {
		return FactSpec{}, err
	}
	fields, err := p.parseValueMap()
	if err != nil {
		return FactSpec{}, err
	}
	key, ok := fields["key"]
	if !ok {
		return FactSpec{}, p.errorf("assert %s requires key", factType)
	}
	delete(fields, "key")
	keyString, ok := key.(string)
	if !ok || keyString == "" {
		return FactSpec{}, p.errorf("assert %s key must be a non-empty string", factType)
	}
	return FactSpec{
		Type:   factType,
		Key:    keyString,
		Fields: fields,
	}, nil
}

func (p *parser) parseExpectation() (Expectation, error) {
	if err := p.consumeKeyword("expect"); err != nil {
		return Expectation{}, err
	}
	p.skipSpace()
	if p.peekKeyword("rule") {
		return p.parseRuleExpectation()
	}
	negated := false
	if p.peekKeyword("no") {
		if err := p.consumeKeyword("no"); err != nil {
			return Expectation{}, err
		}
		negated = true
	}
	switch {
	case p.peekKeyword("action"):
		if negated {
			return Expectation{}, p.errorf("expect no action is not supported")
		}
		return p.parseActionExpectation()
	case p.peekKeyword("flag"):
		if negated {
			return Expectation{}, p.errorf("expect no flag is not supported")
		}
		return p.parseFlagExpectation()
	case p.peekKeyword("fact"):
		return p.parseEntityExpectation(ExpectFact, negated)
	case p.peekKeyword("outcome"):
		return p.parseEntityExpectation(ExpectOutcome, negated)
	default:
		return Expectation{}, p.errorf("expected rule, action, flag, fact, or outcome")
	}
}

func (p *parser) parseRuleExpectation() (Expectation, error) {
	if err := p.consumeKeyword("rule"); err != nil {
		return Expectation{}, err
	}
	target, err := p.parseIdentifier()
	if err != nil {
		return Expectation{}, err
	}
	ruleMatched := true
	switch {
	case p.peekKeyword("matched"):
		if err := p.consumeKeyword("matched"); err != nil {
			return Expectation{}, err
		}
	case p.peekKeyword("not"):
		if err := p.consumeKeyword("not"); err != nil {
			return Expectation{}, err
		}
		if err := p.consumeKeyword("matched"); err != nil {
			return Expectation{}, err
		}
		ruleMatched = false
	default:
		return Expectation{}, p.errorf("expected matched or not matched")
	}
	return Expectation{
		Kind:        ExpectRule,
		Target:      target,
		RuleMatched: ruleMatched,
	}, nil
}

func (p *parser) parseActionExpectation() (Expectation, error) {
	if err := p.consumeKeyword("action"); err != nil {
		return Expectation{}, err
	}
	target, err := p.parseIdentifier()
	if err != nil {
		return Expectation{}, err
	}
	fields, err := p.parseExpectationMap()
	if err != nil {
		return Expectation{}, err
	}
	return Expectation{
		Kind:   ExpectAction,
		Target: target,
		Fields: fields,
	}, nil
}

func (p *parser) parseFlagExpectation() (Expectation, error) {
	if err := p.consumeKeyword("flag"); err != nil {
		return Expectation{}, err
	}
	target, err := p.parseIdentifier()
	if err != nil {
		return Expectation{}, err
	}
	p.skipSpace()
	if !p.consumePrefix("==") {
		return Expectation{}, p.errorf("expected ==")
	}
	raw := p.readValue(true, '\n', '}')
	value, err := parseLiteral(raw)
	if err != nil {
		return Expectation{}, p.errorf("parse literal %q: %v", raw, err)
	}
	return Expectation{
		Kind:   ExpectFlag,
		Target: target,
		Value:  value,
	}, nil
}

func (p *parser) parseEntityExpectation(kind ExpectationKind, negated bool) (Expectation, error) {
	if err := p.consumeKeyword(string(kind)); err != nil {
		return Expectation{}, err
	}
	target, err := p.parseIdentifier()
	if err != nil {
		return Expectation{}, err
	}
	fields := make(map[string]FieldExpectation)
	p.skipSpace()
	if p.peek() == '{' {
		fields, err = p.parseExpectationMap()
		if err != nil {
			return Expectation{}, err
		}
	}
	return Expectation{
		Kind:    kind,
		Target:  target,
		Negated: negated,
		Fields:  fields,
	}, nil
}

func (p *parser) parseValueMap() (map[string]any, error) {
	if err := p.consumeChar('{'); err != nil {
		return nil, err
	}
	fields := make(map[string]any)
	for {
		p.skipSpace()
		if p.consumeIf('}') {
			break
		}
		key, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		if err := p.consumeChar(':'); err != nil {
			return nil, err
		}
		raw := p.readValue(true, ',', '}')
		value, err := parseLiteral(raw)
		if err != nil {
			return nil, p.errorf("parse literal %q: %v", raw, err)
		}
		fields[key] = value
		p.skipSpace()
		p.consumeIf(',')
	}
	return fields, nil
}

func (p *parser) parseExpectationMap() (map[string]FieldExpectation, error) {
	if err := p.consumeChar('{'); err != nil {
		return nil, err
	}
	fields := make(map[string]FieldExpectation)
	for {
		p.skipSpace()
		if p.consumeIf('}') {
			break
		}
		key, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		if err := p.consumeChar(':'); err != nil {
			return nil, err
		}
		expectation, err := p.parseFieldExpectation()
		if err != nil {
			return nil, err
		}
		fields[key] = expectation
		p.skipSpace()
		p.consumeIf(',')
	}
	return fields, nil
}

func (p *parser) parseFieldExpectation() (FieldExpectation, error) {
	p.skipSpace()
	switch {
	case p.peekKeyword("between"):
		if err := p.consumeKeyword("between"); err != nil {
			return FieldExpectation{}, err
		}
		raw := p.readValue(true, ',', '}')
		low, high, err := splitBetweenLiterals(raw)
		if err != nil {
			return FieldExpectation{}, p.errorf("%v", err)
		}
		return FieldExpectation{
			Kind:  FieldBetween,
			Value: low,
			High:  high,
		}, nil
	case p.consumePrefix(">="):
		value, err := p.parseDelimitedLiteral()
		return FieldExpectation{Kind: FieldGte, Value: value}, err
	case p.consumePrefix("<="):
		value, err := p.parseDelimitedLiteral()
		return FieldExpectation{Kind: FieldLte, Value: value}, err
	case p.consumePrefix(">"):
		value, err := p.parseDelimitedLiteral()
		return FieldExpectation{Kind: FieldGt, Value: value}, err
	case p.consumePrefix("<"):
		value, err := p.parseDelimitedLiteral()
		return FieldExpectation{Kind: FieldLt, Value: value}, err
	default:
		value, err := p.parseDelimitedLiteral()
		return FieldExpectation{Kind: FieldExact, Value: value}, err
	}
}

func (p *parser) parseDelimitedLiteral() (any, error) {
	raw := p.readValue(true, ',', '}')
	value, err := parseLiteral(raw)
	if err != nil {
		return nil, p.errorf("parse literal %q: %v", raw, err)
	}
	return value, nil
}

func (p *parser) parseIdentifier() (string, error) {
	p.skipSpace()
	start := p.pos
	if !isIdentStart(p.peek()) {
		return "", p.errorf("expected identifier")
	}
	p.pos++
	for !p.eof() && isIdentContinue(p.peek()) {
		p.pos++
	}
	return p.src[start:p.pos], nil
}

func (p *parser) parsePath() (string, error) {
	first, err := p.parseIdentifier()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(first)
	for p.peek() == '.' {
		p.pos++
		next, err := p.parseIdentifier()
		if err != nil {
			return "", err
		}
		b.WriteByte('.')
		b.WriteString(next)
	}
	return b.String(), nil
}

func (p *parser) parseQuotedString() (string, error) {
	p.skipSpace()
	if p.peek() != '"' {
		return "", p.errorf("expected quoted string")
	}
	start := p.pos
	p.pos++
	escaped := false
	for !p.eof() {
		ch := p.peek()
		p.pos++
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			decoded, err := strconv.Unquote(p.src[start:p.pos])
			if err != nil {
				return "", p.errorf("decode string: %v", err)
			}
			return decoded, nil
		}
	}
	return "", p.errorf("unterminated string")
}

func (p *parser) readValue(stopNewline bool, stops ...byte) string {
	p.skipInlineSpace()
	start := p.pos
	stopSet := make(map[byte]struct{}, len(stops))
	for _, stop := range stops {
		stopSet[stop] = struct{}{}
	}
	depth := 0
	inString := false
	escaped := false
	for !p.eof() {
		ch := p.peek()
		if inString {
			p.pos++
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
			p.pos++
		case '[':
			depth++
			p.pos++
		case ']':
			if depth > 0 {
				depth--
			}
			p.pos++
		case '#':
			if depth == 0 {
				goto end
			}
			p.pos++
		case '\n', '\r':
			if stopNewline && depth == 0 {
				goto end
			}
			p.pos++
		default:
			if depth == 0 {
				if _, ok := stopSet[ch]; ok {
					goto end
				}
			}
			p.pos++
		}
	}
end:
	return strings.TrimSpace(p.src[start:p.pos])
}

func (p *parser) readUntilWhitespaceOr(stop byte) string {
	p.skipSpace()
	start := p.pos
	for !p.eof() {
		ch := p.peek()
		if isWhitespace(ch) || ch == stop {
			break
		}
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) skipSpace() {
	for !p.eof() {
		switch ch := p.peek(); {
		case isWhitespace(ch):
			p.pos++
		case ch == '#':
			p.skipComment()
		default:
			return
		}
	}
}

func (p *parser) skipInlineSpace() {
	for !p.eof() {
		switch ch := p.peek(); {
		case ch == ' ' || ch == '\t':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) skipComment() {
	for !p.eof() && p.peek() != '\n' {
		p.pos++
	}
}

func (p *parser) consumeKeyword(word string) error {
	p.skipSpace()
	if !p.peekKeyword(word) {
		return p.errorf("expected %q", word)
	}
	p.pos += len(word)
	return nil
}

func (p *parser) peekKeyword(word string) bool {
	p.skipSpace()
	if !strings.HasPrefix(p.src[p.pos:], word) {
		return false
	}
	end := p.pos + len(word)
	if end >= len(p.src) {
		return true
	}
	return !isIdentContinue(p.src[end])
}

func (p *parser) consumeChar(ch byte) error {
	p.skipSpace()
	if p.peek() != ch {
		return p.errorf("expected %q", string(ch))
	}
	p.pos++
	return nil
}

func (p *parser) consumeIf(ch byte) bool {
	p.skipSpace()
	if p.peek() != ch {
		return false
	}
	p.pos++
	return true
}

func (p *parser) consumePrefix(prefix string) bool {
	p.skipSpace()
	if strings.HasPrefix(p.src[p.pos:], prefix) {
		p.pos += len(prefix)
		return true
	}
	return false
}

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) eof() bool {
	return p.pos >= len(p.src)
}

func (p *parser) errorf(format string, args ...any) error {
	line, col := lineCol(p.src, p.pos)
	return fmt.Errorf("%s:%d:%d: %s", p.file, line, col, fmt.Sprintf(format, args...))
}

func parseLiteral(text string) (any, error) {
	source := []byte("const __value = " + text + "\n")
	full, err := arbiter.CompileFull(source)
	if err != nil {
		return nil, err
	}
	if full == nil || full.Program == nil || len(full.Program.Consts) == 0 {
		return nil, fmt.Errorf("literal did not produce a const")
	}
	value, ok := ir.LiteralValue(full.Program, full.Program.Consts[0].Value)
	if !ok {
		return nil, fmt.Errorf("unsupported literal %q", text)
	}
	return value, nil
}

func splitBetweenLiterals(raw string) (any, any, error) {
	raw = strings.TrimSpace(raw)
	for i := 0; i < len(raw); i++ {
		if raw[i] != ' ' && raw[i] != '\t' {
			continue
		}
		left := strings.TrimSpace(raw[:i])
		right := strings.TrimSpace(raw[i+1:])
		if left == "" || right == "" {
			continue
		}
		low, err := parseLiteral(left)
		if err != nil {
			continue
		}
		high, err := parseLiteral(right)
		if err != nil {
			continue
		}
		return low, high, nil
	}
	return nil, nil, fmt.Errorf("expected between <low> <high>")
}

func parseStepOffset(text string) (time.Duration, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, fmt.Errorf("missing duration")
	}
	if text == "0" {
		return 0, nil
	}
	unitStart := 0
	for unitStart < len(text) && (text[unitStart] == '.' || text[unitStart] == '-' || (text[unitStart] >= '0' && text[unitStart] <= '9')) {
		unitStart++
	}
	if unitStart == 0 || unitStart == len(text) {
		return 0, fmt.Errorf("invalid duration %q", text)
	}
	value, err := strconv.ParseFloat(text[:unitStart], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", text)
	}
	if value < 0 {
		return 0, fmt.Errorf("duration must be non-negative")
	}
	var scale time.Duration
	switch text[unitStart:] {
	case "ms":
		scale = time.Millisecond
	case "s":
		scale = time.Second
	case "m":
		scale = time.Minute
	case "hr":
		scale = time.Hour
	case "d":
		scale = 24 * time.Hour
	default:
		return 0, fmt.Errorf("unsupported duration unit %q", text[unitStart:])
	}
	return time.Duration(value * float64(scale)), nil
}

func lineCol(src string, pos int) (int, int) {
	line, col := 1, 1
	for i := 0; i < pos && i < len(src); i++ {
		if src[i] == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return line, col
}

func isIdentStart(ch byte) bool {
	return ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}

func isIdentContinue(ch byte) bool {
	return isIdentStart(ch) || (ch >= '0' && ch <= '9')
}

func isWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func mergeMap(dst, src map[string]any) {
	for key, value := range src {
		if nestedSrc, ok := value.(map[string]any); ok {
			nestedDst, _ := dst[key].(map[string]any)
			if nestedDst == nil {
				nestedDst = make(map[string]any)
				dst[key] = nestedDst
			}
			mergeMap(nestedDst, nestedSrc)
			continue
		}
		dst[key] = value
	}
}

func setPath(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := root
	for i := 0; i < len(parts)-1; i++ {
		next, _ := current[parts[i]].(map[string]any)
		if next == nil {
			next = make(map[string]any)
			current[parts[i]] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}
