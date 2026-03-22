package arbiter

import (
	"testing"
	"time"

	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/intern"
	"github.com/odvcencio/arbiter/units"
)

// Helper that compiles .arb source and evals against the given data context.
// Returns true if any rule matched (non-fallback).
func evalRule(t *testing.T, source string, data map[string]any) bool {
	t.Helper()
	rs, err := Compile([]byte(source))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	dc := DataFromMap(data, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	return len(matched) > 0
}

func makeBytecodeRuleSet(pool *intern.Pool, code []byte, ruleName string) *compiler.CompiledRuleset {
	nameIdx := pool.String(ruleName)
	actionIdx := uint16(0)
	actionNameIdx := pool.String("Act")
	return &compiler.CompiledRuleset{
		Constants:    pool,
		Instructions: code,
		Rules: []compiler.RuleHeader{{
			NameIdx:      nameIdx,
			Priority:     1,
			ConditionOff: 0,
			ConditionLen: uint32(len(code)),
			ActionIdx:    actionIdx,
		}},
		Actions: []compiler.ActionEntry{{
			NameIdx: actionNameIdx,
		}},
	}
}

// === Comparison Operators ===

func TestEvalOpEq(t *testing.T) {
	src := `rule T { when { x == 1 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 1.0}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"x": 2.0}) {
		t.Error("should not match")
	}
}

func TestEvalOpNeq(t *testing.T) {
	src := `rule T { when { x != 1 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 2.0}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"x": 1.0}) {
		t.Error("should not match")
	}
}

func TestEvalOpGt(t *testing.T) {
	src := `rule T { when { x > 10 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 11.0}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("should not match at boundary")
	}
	if evalRule(t, src, map[string]any{"x": 9.0}) {
		t.Error("should not match below")
	}
}

func TestEvalOpGte(t *testing.T) {
	src := `rule T { when { x >= 10 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("should match at boundary")
	}
	if !evalRule(t, src, map[string]any{"x": 11.0}) {
		t.Error("should match above")
	}
	if evalRule(t, src, map[string]any{"x": 9.0}) {
		t.Error("should not match")
	}
}

func TestEvalOpLt(t *testing.T) {
	src := `rule T { when { x < 10 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 9.0}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("should not match at boundary")
	}
	if evalRule(t, src, map[string]any{"x": 11.0}) {
		t.Error("should not match above")
	}
}

func TestEvalOpLte(t *testing.T) {
	src := `rule T { when { x <= 10 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("should match at boundary")
	}
	if !evalRule(t, src, map[string]any{"x": 9.0}) {
		t.Error("should match below")
	}
	if evalRule(t, src, map[string]any{"x": 11.0}) {
		t.Error("should not match")
	}
}

func TestEvalOpDecimalComparison(t *testing.T) {
	src := `rule T { when { 1000.25 USD >= 1000.25 USD } then A {} }`
	if !evalRule(t, src, nil) {
		t.Error("expected exact decimal comparison to match")
	}
}

func TestEvalOpDecimalArithmetic(t *testing.T) {
	src := `rule T { when { abs(-1000.25 USD + 10.00 USD) == 990.25 USD } then A {} }`
	if !evalRule(t, src, nil) {
		t.Error("expected decimal arithmetic to match")
	}
}

// === String Comparison ===

func TestEvalStringEq(t *testing.T) {
	src := `rule T { when { name == "alice" } then A {} }`
	if !evalRule(t, src, map[string]any{"name": "alice"}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"name": "bob"}) {
		t.Error("should not match")
	}
}

func TestEvalStringNeq(t *testing.T) {
	src := `rule T { when { name != "alice" } then A {} }`
	if !evalRule(t, src, map[string]any{"name": "bob"}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"name": "alice"}) {
		t.Error("should not match")
	}
}

func TestEvalStringConcat(t *testing.T) {
	src := `rule T { when { true } then Act { message: "Hello " + user.name } }`
	rs, err := Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{
		"user": map[string]any{"name": "World"},
	}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Params["message"] != "Hello World" {
		t.Fatalf("message: got %v, want %q", matched[0].Params["message"], "Hello World")
	}
}

func TestEvalQuantityComparison(t *testing.T) {
	src := `rule T { when { sensor.temp > 28 C } then A {} }`
	rs, err := Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{
		"sensor": map[string]any{
			"temp": units.Quantity{Value: 86, Unit: "F"},
		},
	}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
}

func TestEvalBuiltinMathFunctions(t *testing.T) {
	src := `rule T { when { abs(sensor.delta) > 5 and max(sensor.left, sensor.right) == 9 and min(sensor.left, sensor.right) == 3 and round(sensor.ratio) == 4 and floor(sensor.flooring) == 4 and ceil(sensor.ceiling) == 5 } then A {} }`
	if !evalRule(t, src, map[string]any{
		"sensor": map[string]any{
			"delta":    -6.0,
			"left":     3.0,
			"right":    9.0,
			"ratio":    3.6,
			"flooring": 4.9,
			"ceiling":  4.1,
		},
	}) {
		t.Fatal("expected builtin math functions to evaluate correctly")
	}
}

func TestEvalTimestampLiteralAndNowBuiltin(t *testing.T) {
	cutoff := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	src := `rule T { when { now() > 2026-01-01T00:00:00Z } then A {} }`
	if !evalRule(t, src, map[string]any{"__now": float64(cutoff.Add(24 * time.Hour).Unix())}) {
		t.Fatal("expected now() to compare against timestamp literal")
	}
	if evalRule(t, src, map[string]any{"__now": float64(cutoff.Unix())}) {
		t.Fatal("expected now() at cutoff to not satisfy > comparison")
	}
}

func TestEvalTimestampAdditionWithDuration(t *testing.T) {
	recorded := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	src := `rule T { when { recorded + 5m > now() } then A {} }`
	if !evalRule(t, src, map[string]any{
		"recorded": float64(recorded.Unix()),
		"__now":    float64(recorded.Add(4 * time.Minute).Unix()),
	}) {
		t.Fatal("expected timestamp + duration to compare against now()")
	}
	if evalRule(t, src, map[string]any{
		"recorded": float64(recorded.Unix()),
		"__now":    float64(recorded.Add(6 * time.Minute).Unix()),
	}) {
		t.Fatal("expected timestamp + duration comparison to fail after expiry")
	}
}

// === Logical Operators ===

func TestEvalAnd(t *testing.T) {
	src := `rule T { when { x > 1 and y > 1 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 2.0, "y": 2.0}) {
		t.Error("both true should match")
	}
	if evalRule(t, src, map[string]any{"x": 2.0, "y": 0.0}) {
		t.Error("right false should not match")
	}
	if evalRule(t, src, map[string]any{"x": 0.0, "y": 2.0}) {
		t.Error("left false should not match")
	}
	if evalRule(t, src, map[string]any{"x": 0.0, "y": 0.0}) {
		t.Error("both false should not match")
	}
}

func TestEvalOr(t *testing.T) {
	src := `rule T { when { x > 1 or y > 1 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 2.0, "y": 2.0}) {
		t.Error("both true should match")
	}
	if !evalRule(t, src, map[string]any{"x": 2.0, "y": 0.0}) {
		t.Error("left true should match")
	}
	if !evalRule(t, src, map[string]any{"x": 0.0, "y": 2.0}) {
		t.Error("right true should match")
	}
	if evalRule(t, src, map[string]any{"x": 0.0, "y": 0.0}) {
		t.Error("both false should not match")
	}
}

func TestEvalNot(t *testing.T) {
	src := `rule T { when { not x == 1 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 2.0}) {
		t.Error("should match when x != 1")
	}
	if evalRule(t, src, map[string]any{"x": 1.0}) {
		t.Error("should not match when x == 1")
	}
}

func TestEvalThreeWayAnd(t *testing.T) {
	src := `rule T { when { a > 1 and b > 1 and c > 1 } then A {} }`
	if !evalRule(t, src, map[string]any{"a": 2.0, "b": 2.0, "c": 2.0}) {
		t.Error("all true should match")
	}
	if evalRule(t, src, map[string]any{"a": 2.0, "b": 2.0, "c": 0.0}) {
		t.Error("one false should not match")
	}
}

func TestEvalParenGrouping(t *testing.T) {
	src := `rule T { when { (a > 1 or b > 1) and c > 1 } then A {} }`
	if !evalRule(t, src, map[string]any{"a": 2.0, "b": 0.0, "c": 2.0}) {
		t.Error("(true or false) and true should match")
	}
	if evalRule(t, src, map[string]any{"a": 0.0, "b": 0.0, "c": 2.0}) {
		t.Error("(false or false) and true should not match")
	}
	if evalRule(t, src, map[string]any{"a": 2.0, "b": 0.0, "c": 0.0}) {
		t.Error("(true or false) and false should not match")
	}
}

// === Collection Operators ===

func TestEvalIn(t *testing.T) {
	src := `rule T { when { role in ["admin", "mod"] } then A {} }`
	if !evalRule(t, src, map[string]any{"role": "admin"}) {
		t.Error("should match admin")
	}
	if !evalRule(t, src, map[string]any{"role": "mod"}) {
		t.Error("should match mod")
	}
	if evalRule(t, src, map[string]any{"role": "user"}) {
		t.Error("should not match user")
	}
}

func TestEvalNotIn(t *testing.T) {
	src := `rule T { when { role not in ["banned", "suspended"] } then A {} }`
	if !evalRule(t, src, map[string]any{"role": "admin"}) {
		t.Error("should match admin")
	}
	if evalRule(t, src, map[string]any{"role": "banned"}) {
		t.Error("should not match banned")
	}
	if evalRule(t, src, map[string]any{"role": "suspended"}) {
		t.Error("should not match suspended")
	}
}

func TestEvalInNumeric(t *testing.T) {
	src := `rule T { when { code in [100, 200, 300] } then A {} }`
	if !evalRule(t, src, map[string]any{"code": 200.0}) {
		t.Error("should match 200")
	}
	if evalRule(t, src, map[string]any{"code": 404.0}) {
		t.Error("should not match 404")
	}
}

// === String Operators ===

func TestEvalStartsWith(t *testing.T) {
	src := `rule T { when { name starts_with "Dr" } then A {} }`
	if !evalRule(t, src, map[string]any{"name": "Dr. Smith"}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"name": "Mr. Jones"}) {
		t.Error("should not match")
	}
}

func TestEvalEndsWith(t *testing.T) {
	src := `rule T { when { email ends_with ".edu" } then A {} }`
	if !evalRule(t, src, map[string]any{"email": "alice@mit.edu"}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"email": "bob@gmail.com"}) {
		t.Error("should not match")
	}
}

func TestEvalMatches(t *testing.T) {
	src := `rule T { when { code matches "^[A-Z]{3}$" } then A {} }`
	if !evalRule(t, src, map[string]any{"code": "ABC"}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"code": "abcd"}) {
		t.Error("should not match")
	}
}

func TestEvalMatchesDigits(t *testing.T) {
	src := `rule T { when { zip matches "^[0-9]{5}$" } then A {} }`
	if !evalRule(t, src, map[string]any{"zip": "90210"}) {
		t.Error("should match 5-digit zip")
	}
	if evalRule(t, src, map[string]any{"zip": "9021"}) {
		t.Error("should not match 4 digits")
	}
}

// === Null Operators ===

func TestEvalIsNull(t *testing.T) {
	src := `rule T { when { x is null } then A {} }`
	if !evalRule(t, src, map[string]any{}) {
		t.Error("missing key should be null")
	}
	if evalRule(t, src, map[string]any{"x": 1.0}) {
		t.Error("present key should not be null")
	}
}

func TestEvalIsNotNull(t *testing.T) {
	src := `rule T { when { x is not null } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 1.0}) {
		t.Error("should match present key")
	}
	if evalRule(t, src, map[string]any{}) {
		t.Error("should not match missing key")
	}
}

// === Math Operators ===

func TestEvalMathAdd(t *testing.T) {
	src := `rule T { when { x + y > 10 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 6.0, "y": 5.0}) {
		t.Error("6+5=11 > 10 should match")
	}
	if evalRule(t, src, map[string]any{"x": 3.0, "y": 4.0}) {
		t.Error("3+4=7 not > 10 should not match")
	}
}

func TestEvalMathSub(t *testing.T) {
	src := `rule T { when { x - y > 5 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 20.0, "y": 10.0}) {
		t.Error("20-10=10 > 5 should match")
	}
	if evalRule(t, src, map[string]any{"x": 8.0, "y": 5.0}) {
		t.Error("8-5=3 not > 5 should not match")
	}
}

func TestEvalMathMul(t *testing.T) {
	src := `rule T { when { price * qty > 100 } then A {} }`
	if !evalRule(t, src, map[string]any{"price": 25.0, "qty": 5.0}) {
		t.Error("25*5=125 > 100 should match")
	}
	if evalRule(t, src, map[string]any{"price": 10.0, "qty": 5.0}) {
		t.Error("10*5=50 not > 100 should not match")
	}
}

func TestEvalMathDiv(t *testing.T) {
	src := `rule T { when { total / count > 50 } then A {} }`
	if !evalRule(t, src, map[string]any{"total": 200.0, "count": 2.0}) {
		t.Error("200/2=100 > 50 should match")
	}
	if evalRule(t, src, map[string]any{"total": 50.0, "count": 2.0}) {
		t.Error("50/2=25 not > 50 should not match")
	}
}

func TestEvalMathMod(t *testing.T) {
	src := `rule T { when { x % 2 == 0 } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("10 % 2 == 0 should match")
	}
	if evalRule(t, src, map[string]any{"x": 7.0}) {
		t.Error("7 % 2 == 1 should not match")
	}
}

// === Range (Between) ===

func TestEvalBetweenClosed(t *testing.T) {
	src := `rule T { when { x between [10, 20] } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("lower boundary should match (closed)")
	}
	if !evalRule(t, src, map[string]any{"x": 15.0}) {
		t.Error("middle should match")
	}
	if !evalRule(t, src, map[string]any{"x": 20.0}) {
		t.Error("upper boundary should match (closed)")
	}
	if evalRule(t, src, map[string]any{"x": 9.0}) {
		t.Error("below should not match")
	}
	if evalRule(t, src, map[string]any{"x": 21.0}) {
		t.Error("above should not match")
	}
}

func TestEvalBetweenOpen(t *testing.T) {
	src := `rule T { when { x between (10, 20) } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 15.0}) {
		t.Error("middle should match")
	}
	if evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("lower boundary should not match (open)")
	}
	if evalRule(t, src, map[string]any{"x": 20.0}) {
		t.Error("upper boundary should not match (open)")
	}
}

func TestEvalBetweenClosedOpen(t *testing.T) {
	src := `rule T { when { x between [10, 20) } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("lower boundary should match (closed)")
	}
	if evalRule(t, src, map[string]any{"x": 20.0}) {
		t.Error("upper boundary should not match (open)")
	}
}

func TestEvalBetweenOpenClosed(t *testing.T) {
	src := `rule T { when { x between (10, 20] } then A {} }`
	if evalRule(t, src, map[string]any{"x": 10.0}) {
		t.Error("lower boundary should not match (open)")
	}
	if !evalRule(t, src, map[string]any{"x": 20.0}) {
		t.Error("upper boundary should match (closed)")
	}
}

// === Boolean Literal ===

func TestEvalBoolTrue(t *testing.T) {
	src := `rule T { when { true } then A {} }`
	if !evalRule(t, src, map[string]any{}) {
		t.Error("true should always match")
	}
}

func TestEvalBoolFalse(t *testing.T) {
	src := `rule T { when { false } then A {} }`
	if evalRule(t, src, map[string]any{}) {
		t.Error("false should never match")
	}
}

func TestEvalBoolComparison(t *testing.T) {
	src := `rule T { when { active == true } then A {} }`
	if !evalRule(t, src, map[string]any{"active": true}) {
		t.Error("active=true should match")
	}
	if evalRule(t, src, map[string]any{"active": false}) {
		t.Error("active=false should not match")
	}
}

// === Constants ===

func TestEvalConstInlining(t *testing.T) {
	src := `const LIMIT = 100
rule T { when { x > LIMIT } then A {} }`
	if !evalRule(t, src, map[string]any{"x": 101.0}) {
		t.Error("should match above LIMIT")
	}
	if evalRule(t, src, map[string]any{"x": 99.0}) {
		t.Error("should not match below LIMIT")
	}
}

func TestEvalConstList(t *testing.T) {
	src := `const ADMINS = ["alice", "bob"]
rule T { when { name in ADMINS } then A {} }`
	if !evalRule(t, src, map[string]any{"name": "alice"}) {
		t.Error("should match alice")
	}
	if !evalRule(t, src, map[string]any{"name": "bob"}) {
		t.Error("should match bob")
	}
	if evalRule(t, src, map[string]any{"name": "charlie"}) {
		t.Error("should not match charlie")
	}
}

func TestEvalConstString(t *testing.T) {
	src := `const PREFIX = "Dr"
rule T { when { name starts_with PREFIX } then A {} }`
	if !evalRule(t, src, map[string]any{"name": "Dr. Smith"}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"name": "Mr. Jones"}) {
		t.Error("should not match")
	}
}

// === Nested/Complex ===

func TestEvalNestedDotNotation(t *testing.T) {
	src := `rule T { when { user.age > 18 } then A {} }`
	if !evalRule(t, src, map[string]any{"user": map[string]any{"age": 25.0}}) {
		t.Error("should match")
	}
	if evalRule(t, src, map[string]any{"user": map[string]any{"age": 16.0}}) {
		t.Error("should not match")
	}
}

func TestEvalDeepNesting(t *testing.T) {
	src := `rule T { when { a.b.c > 0 } then A {} }`
	data := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": 5.0,
			},
		},
	}
	if !evalRule(t, src, data) {
		t.Error("deep nested access should match")
	}
}

func TestEvalComplexCondition(t *testing.T) {
	src := `rule T {
    when {
        user.age >= 18
        and user.country != "XX"
        and user.score > 50
    }
    then Approve {}
}`
	match := map[string]any{"user": map[string]any{"age": 25.0, "country": "US", "score": 75.0}}
	if !evalRule(t, src, match) {
		t.Error("should match")
	}

	blocked := map[string]any{"user": map[string]any{"age": 25.0, "country": "XX", "score": 75.0}}
	if evalRule(t, src, blocked) {
		t.Error("blocked country should not match")
	}

	young := map[string]any{"user": map[string]any{"age": 16.0, "country": "US", "score": 75.0}}
	if evalRule(t, src, young) {
		t.Error("underage should not match")
	}

	lowScore := map[string]any{"user": map[string]any{"age": 25.0, "country": "US", "score": 30.0}}
	if evalRule(t, src, lowScore) {
		t.Error("low score should not match")
	}
}

// === Fallback (otherwise) ===

func TestEvalFallbackAction(t *testing.T) {
	src := `rule T {
    when { x > 100 }
    then High {}
    otherwise Low {}
}`
	rs, err := Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	// x > 100 -> High (not fallback)
	dc := DataFromMap(map[string]any{"x": 200.0}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Fallback {
		t.Error("should not be fallback")
	}
	if matched[0].Action != "High" {
		t.Errorf("action: got %q, want High", matched[0].Action)
	}

	// x <= 100 -> Low (fallback)
	dc = DataFromMap(map[string]any{"x": 50.0}, rs)
	matched, err = Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (fallback), got %d", len(matched))
	}
	if !matched[0].Fallback {
		t.Error("should be fallback")
	}
	if matched[0].Action != "Low" {
		t.Errorf("action: got %q, want Low", matched[0].Action)
	}
}

// === Action Parameters ===

func TestEvalActionParams(t *testing.T) {
	src := `rule T { when { true } then Act { level: "admin", ttl: 3600 } }`
	rs, err := Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Action != "Act" {
		t.Errorf("action: got %q, want Act", matched[0].Action)
	}
	if matched[0].Params["level"] != "admin" {
		t.Errorf("param level: got %v, want admin", matched[0].Params["level"])
	}
	if matched[0].Params["ttl"] != float64(3600) {
		t.Errorf("param ttl: got %v, want 3600", matched[0].Params["ttl"])
	}
}

func TestEvalSetLocalBytecode(t *testing.T) {
	pool := intern.NewPool()
	tempIdx := pool.String("temp")

	var code []byte
	code = compiler.Emit(code, compiler.OpLoadNum, 0, pool.Number(42))
	code = compiler.Emit(code, compiler.OpSetLocal, 0, tempIdx)
	code = compiler.Emit(code, compiler.OpLoadVar, 0, tempIdx)
	code = compiler.Emit(code, compiler.OpLoadNum, 0, pool.Number(42))
	code = compiler.Emit(code, compiler.OpEq, 0, 0)
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)

	rs := makeBytecodeRuleSet(pool, code, "SetLocal")
	dc := DataFromMap(map[string]any{}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
}

func TestEvalAggregateBytecode(t *testing.T) {
	pool := intern.NewPool()
	itemIdx := pool.String("item")
	itemsIdx := pool.String("items")
	priceIdx := pool.String("item.price")

	var code []byte
	code = compiler.Emit(code, compiler.OpLoadVar, 0, itemsIdx)
	code = compiler.Emit(code, compiler.OpAggBegin, compiler.FlagSum, itemIdx)
	bodyStart := len(code)
	code = compiler.Emit(code, compiler.OpLoadVar, 0, priceIdx)
	bodyLen := uint16(len(code) - bodyStart)
	code = compiler.Emit(code, compiler.OpAggAccum, compiler.FlagSum, bodyLen)
	code = compiler.Emit(code, compiler.OpAggEnd, compiler.FlagSum, 0)
	code = compiler.Emit(code, compiler.OpLoadNum, 0, pool.Number(100))
	code = compiler.Emit(code, compiler.OpGt, 0, 0)
	code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)

	rs := makeBytecodeRuleSet(pool, code, "AggSum")
	dc := DataFromMap(map[string]any{
		"items": []any{
			map[string]any{"price": 50.0},
			map[string]any{"price": 60.0},
		},
	}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
}

func TestEvalAggregateCountAndAvgBytecode(t *testing.T) {
	pool := intern.NewPool()
	itemIdx := pool.String("item")
	valuesIdx := pool.String("values")
	scoreIdx := pool.String("item.score")

	var countCode []byte
	countCode = compiler.Emit(countCode, compiler.OpLoadVar, 0, valuesIdx)
	countCode = compiler.Emit(countCode, compiler.OpAggBegin, compiler.FlagCount, itemIdx)
	countBodyStart := len(countCode)
	countCode = compiler.Emit(countCode, compiler.OpLoadNum, 0, pool.Number(1))
	countBodyLen := uint16(len(countCode) - countBodyStart)
	countCode = compiler.Emit(countCode, compiler.OpAggAccum, compiler.FlagCount, countBodyLen)
	countCode = compiler.Emit(countCode, compiler.OpAggEnd, compiler.FlagCount, 0)
	countCode = compiler.Emit(countCode, compiler.OpLoadNum, 0, pool.Number(3))
	countCode = compiler.Emit(countCode, compiler.OpEq, 0, 0)
	countCode = compiler.Emit(countCode, compiler.OpRuleMatch, 0, 0)

	countRS := makeBytecodeRuleSet(pool, countCode, "AggCount")
	countDC := DataFromMap(map[string]any{
		"values": []any{1.0, 2.0, 3.0},
	}, countRS)
	countMatched, err := Eval(countRS, countDC)
	if err != nil {
		t.Fatal(err)
	}
	if len(countMatched) != 1 {
		t.Fatalf("count: expected 1 match, got %d", len(countMatched))
	}

	var avgCode []byte
	avgCode = compiler.Emit(avgCode, compiler.OpLoadVar, 0, valuesIdx)
	avgCode = compiler.Emit(avgCode, compiler.OpAggBegin, compiler.FlagAvg, itemIdx)
	avgBodyStart := len(avgCode)
	avgCode = compiler.Emit(avgCode, compiler.OpLoadVar, 0, scoreIdx)
	avgBodyLen := uint16(len(avgCode) - avgBodyStart)
	avgCode = compiler.Emit(avgCode, compiler.OpAggAccum, compiler.FlagAvg, avgBodyLen)
	avgCode = compiler.Emit(avgCode, compiler.OpAggEnd, compiler.FlagAvg, 0)
	avgCode = compiler.Emit(avgCode, compiler.OpLoadNum, 0, pool.Number(7))
	avgCode = compiler.Emit(avgCode, compiler.OpGt, 0, 0)
	avgCode = compiler.Emit(avgCode, compiler.OpRuleMatch, 0, 0)

	avgRS := makeBytecodeRuleSet(pool, avgCode, "AggAvg")
	avgDC := DataFromMap(map[string]any{
		"values": []any{
			map[string]any{"score": 8.0},
			map[string]any{"score": 9.0},
			map[string]any{"score": 6.0},
		},
	}, avgRS)
	avgMatched, err := Eval(avgRS, avgDC)
	if err != nil {
		t.Fatal(err)
	}
	if len(avgMatched) != 1 {
		t.Fatalf("avg: expected 1 match, got %d", len(avgMatched))
	}
}

// === Geofencing (real-world) ===

func TestEvalGeofencing(t *testing.T) {
	src := `rule InSF {
    when {
        user.lat between [37.7, 37.8]
        and user.lng between [-122.5, -122.4]
    }
    then Match {}
}`
	sf := map[string]any{"user": map[string]any{"lat": 37.75, "lng": -122.45}}
	if !evalRule(t, src, sf) {
		t.Error("point in SF should match")
	}
	nyc := map[string]any{"user": map[string]any{"lat": 40.7, "lng": -74.0}}
	if evalRule(t, src, nyc) {
		t.Error("point in NYC should not match")
	}
}

// === Priority ===

func TestEvalPriorityOrder(t *testing.T) {
	src := `
rule Low priority 10 { when { true } then Low {} }
rule High priority 1 { when { true } then High {} }
rule Mid priority 5 { when { true } then Mid {} }
`
	rs, err := Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) < 3 {
		t.Fatalf("expected 3 matches, got %d", len(matched))
	}
	names := map[string]bool{}
	for _, m := range matched {
		names[m.Name] = true
	}
	if !names["High"] || !names["Mid"] || !names["Low"] {
		t.Error("expected all three rules to match")
	}
}

// === Multiple Rules ===

func TestEvalMultipleRulesSelectiveMatch(t *testing.T) {
	src := `
rule A { when { x > 10 } then ActionA {} }
rule B { when { x > 20 } then ActionB {} }
rule C { when { x > 30 } then ActionC {} }
`
	rs, err := Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	// x=25 should match A and B but not C
	dc := DataFromMap(map[string]any{"x": 25.0}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 2 {
		t.Fatalf("expected 2 matches for x=25, got %d", len(matched))
	}
	names := map[string]bool{}
	for _, m := range matched {
		names[m.Action] = true
	}
	if !names["ActionA"] || !names["ActionB"] {
		t.Error("expected ActionA and ActionB to match")
	}
	if names["ActionC"] {
		t.Error("ActionC should not match for x=25")
	}
}

// === Variable-to-Variable Comparison ===

func TestEvalVarToVarComparison(t *testing.T) {
	src := `rule T { when { actual > expected } then A {} }`
	if !evalRule(t, src, map[string]any{"actual": 20.0, "expected": 10.0}) {
		t.Error("20 > 10 should match")
	}
	if evalRule(t, src, map[string]any{"actual": 5.0, "expected": 10.0}) {
		t.Error("5 > 10 should not match")
	}
}

// === Contains ===

func TestEvalContains(t *testing.T) {
	src := `rule T { when { tags contains "vip" } then A {} }`
	if !evalRule(t, src, map[string]any{"tags": []string{"vip", "premium"}}) {
		t.Error("expected contains to match runtime list values")
	}
}

func TestEvalRetains(t *testing.T) {
	src := `rule T { when { ["vip", "pro"] retains ["pro", "gold"] } then A {} }`
	if !evalRule(t, src, map[string]any{}) {
		t.Error("expected retains to match overlapping lists")
	}
}

func TestEvalSubsetOf(t *testing.T) {
	src := `rule T { when { ["vip"] subset_of ["vip", "pro"] } then A {} }`
	if !evalRule(t, src, map[string]any{}) {
		t.Error("expected subset_of to match")
	}
}

func TestEvalSupersetOf(t *testing.T) {
	src := `rule T { when { ["vip", "pro"] superset_of ["vip"] } then A {} }`
	if !evalRule(t, src, map[string]any{}) {
		t.Error("expected superset_of to match")
	}
}

func TestEvalVagueContains(t *testing.T) {
	src := `rule T { when { ["premium", "standard"] vague_contains "mium" } then A {} }`
	if !evalRule(t, src, map[string]any{}) {
		t.Error("expected vague_contains to match substring in list element")
	}
}

func TestEvalQuantifierAny(t *testing.T) {
	src := `rule T { when { any item in items { item > 0 } } then A {} }`
	if !evalRule(t, src, map[string]any{"items": []any{-1.0, 2.0}}) {
		t.Error("expected any quantifier to match")
	}
}

func TestEvalQuantifierAll(t *testing.T) {
	src := `rule T { when { all item in items { item > 0 } } then A {} }`
	if !evalRule(t, src, map[string]any{"items": []any{1.0, 2.0}}) {
		t.Error("expected all quantifier to match")
	}
	if evalRule(t, src, map[string]any{"items": []any{1.0, -1.0}}) {
		t.Error("expected all quantifier to fail when one item fails")
	}
}

func TestEvalQuantifierNone(t *testing.T) {
	src := `rule T { when { none item in items { item > 0 } } then A {} }`
	if !evalRule(t, src, map[string]any{"items": []any{-1.0, -2.0}}) {
		t.Error("expected none quantifier to match")
	}
	if evalRule(t, src, map[string]any{"items": []any{-1.0, 2.0}}) {
		t.Error("expected none quantifier to fail when one item matches")
	}
}

func TestEvalQuantifierMemberAccess(t *testing.T) {
	src := `rule T { when { any item in cart.items { item.price > 100 } } then A {} }`
	data := map[string]any{
		"cart": map[string]any{
			"items": []any{
				map[string]any{"price": 50.0},
				map[string]any{"price": 150.0},
			},
		},
	}
	if !evalRule(t, src, data) {
		t.Error("expected quantifier locals to resolve nested member access")
	}
}

// === Math with Variable on RHS ===

func TestEvalMathVarMul(t *testing.T) {
	src := `rule T { when { amount > avg * 5 } then A {} }`
	if !evalRule(t, src, map[string]any{"amount": 600.0, "avg": 100.0}) {
		t.Error("600 > 100*5=500 should match")
	}
	if evalRule(t, src, map[string]any{"amount": 400.0, "avg": 100.0}) {
		t.Error("400 > 100*5=500 should not match")
	}
}

// === Missing Variable Behavior ===

func TestEvalMissingVarNumericComparison(t *testing.T) {
	src := `rule T { when { x > 10 } then A {} }`
	// missing x should resolve to null, toNum returns 0, so 0 > 10 = false
	if evalRule(t, src, map[string]any{}) {
		t.Error("missing variable should not match numeric comparison")
	}
}

func TestEvalMissingVarStringComparison(t *testing.T) {
	src := `rule T { when { name == "alice" } then A {} }`
	// missing name should resolve to null, types differ from string, so false
	if evalRule(t, src, map[string]any{}) {
		t.Error("missing variable should not match string comparison")
	}
}

// === Real-World: Pricing Rules ===

func TestEvalPricingFreeShipping(t *testing.T) {
	src := `const FREE_SHIP_MIN = 35
rule FreeShipping priority 1 {
    when {
        user.cart_total >= FREE_SHIP_MIN
        and user.region != "XX"
    }
    then ApplyShipping {
        cost: 0,
    }
    otherwise ApplyShipping {
        cost: 5.99,
    }
}`
	// Qualifies for free shipping
	rs, err := Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{
		"user": map[string]any{"cart_total": 50.0, "region": "US"},
	}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Fallback {
		t.Error("should not be fallback for qualifying order")
	}
	if matched[0].Action != "ApplyShipping" {
		t.Errorf("expected ApplyShipping, got %s", matched[0].Action)
	}

	// Below threshold -> fallback
	dc = DataFromMap(map[string]any{
		"user": map[string]any{"cart_total": 20.0, "region": "US"},
	}, rs)
	matched, err = Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (fallback), got %d", len(matched))
	}
	if !matched[0].Fallback {
		t.Error("should be fallback for order below threshold")
	}
}

// === Real-World: Fraud Detection ===

func TestEvalFraudInstantBlock(t *testing.T) {
	src := `rule InstantBlock priority 0 {
    when {
        account.flagged == true
        or model.risk_score > 0.95
    }
    then Block {
        reason: "flagged or extreme risk",
    }
}`
	// Flagged account
	if !evalRule(t, src, map[string]any{
		"account": map[string]any{"flagged": true},
		"model":   map[string]any{"risk_score": 0.1},
	}) {
		t.Error("flagged account should trigger block")
	}

	// High risk score
	if !evalRule(t, src, map[string]any{
		"account": map[string]any{"flagged": false},
		"model":   map[string]any{"risk_score": 0.99},
	}) {
		t.Error("extreme risk score should trigger block")
	}

	// Neither flagged nor high risk
	if evalRule(t, src, map[string]any{
		"account": map[string]any{"flagged": false},
		"model":   map[string]any{"risk_score": 0.5},
	}) {
		t.Error("normal account should not trigger block")
	}
}
