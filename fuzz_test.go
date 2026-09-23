package arbiter

import (
	"testing"
)

func FuzzCompile(f *testing.F) {
	// Seed with valid .arb patterns.
	f.Add([]byte(`rule X { when { a > 1 } then Y { z: 1 } }`))
	f.Add([]byte(`flag f type boolean default "false" { when { true } then "true" }`))
	f.Add([]byte(`segment s { x == "y" }`))
	f.Add([]byte(`const C = 42`))
	f.Add([]byte(`fact F { key: string value: number }`))
	f.Add([]byte(`outcome O { status: string }`))
	f.Add([]byte(`strategy S returns O { when { x > 0 } then A { status: "ok" } else B { status: "default" } }`))
	f.Add([]byte(`expert rule E { when { x > 0 } then assert F { key: "k" } }`))
	f.Add([]byte(`include "nonexistent.arb"`))
	f.Add([]byte(``))
	f.Add([]byte(`rule { }`))
	f.Add([]byte(`rule X { when { } then Y { } }`))
	// Constant reference cycles used to overflow the stack (fatal, unrecoverable)
	// in validateExpr's ExprConstRef case; see TestConstSelfCycleRejected et al.
	f.Add([]byte(`const C = C
rule R { when { user.score >= C } then Deny {} }`))
	f.Add([]byte(`const A = B
const B = A
rule R { when { user.score >= A } then Deny {} }`))
	f.Add([]byte(`const A = B + 1
const B = A
rule R { when { user.score >= A } then Deny {} }`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic on any input.
		CompileFull(data)
	})
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`rule X { when { a > 1 and b < 2 or c == "d" } then Y { z: a + b * 3 } }`))
	f.Add([]byte(`expert rule E per_fact { when { any x in facts.F { x.v > 0 } } for 10m then emit O { key: "k" } }`))
	f.Add([]byte(`arbiter A { poll 5s stream ws source http on * stdout }`))
	f.Add([]byte{0xFF, 0xFE, 0x00, 0x01})
	f.Add([]byte(`rule "unterminated string`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic on any input.
		ParseSource(data)
	})
}

func FuzzEval(f *testing.F) {
	f.Add(`{"a": 1, "b": "hello", "c": true}`)
	f.Add(`{"order": {"total": 99.5, "items": 3}}`)
	f.Add(`{}`)
	f.Add(`{"x": null}`)
	f.Add(`{"deep": {"nested": {"value": 42}}}`)

	prog, err := Compile([]byte(`
rule High { when { order.total > 100 } then Flag { level: "high" } }
rule Low { when { order.total <= 100 } then Flag { level: "low" } }
`))
	if err != nil {
		f.Fatalf("compile: %v", err)
	}
	f.Fuzz(func(t *testing.T, jsonStr string) {
		dc, err := DataFromJSON(jsonStr, prog)
		if err != nil {
			return // Invalid JSON is expected.
		}
		// Must not panic on any valid JSON context.
		Eval(prog, dc)
	})
}
