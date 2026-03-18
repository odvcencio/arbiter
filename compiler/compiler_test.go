package compiler_test

import (
	"os"
	"testing"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/intern"
	gotreesitter "github.com/odvcencio/gotreesitter"
)

// helper: parse and compile .arb source
func compileSource(t *testing.T, source string) *compiler.CompiledRuleset {
	t.Helper()
	lang, err := arbiter.GetLanguage()
	if err != nil {
		t.Fatalf("GetLanguage: %v", err)
	}

	parser := gotreesitter.NewParser(lang)
	src := []byte(source)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	root := tree.RootNode()
	rs, err := compiler.CompileCST(root, src, lang)
	if err != nil {
		t.Fatalf("CompileCST: %v", err)
	}
	return rs
}

func TestCompileSimpleEquality(t *testing.T) {
	src := `
rule Check {
    when { x == 42 }
    then Pass {}
}
`
	rs := compileSource(t, src)

	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}

	rh := rs.Rules[0]
	name := rs.Constants.GetString(rh.NameIdx)
	if name != "Check" {
		t.Errorf("rule name = %q, want %q", name, "Check")
	}

	if rh.ConditionLen == 0 {
		t.Fatal("condition bytecode is empty")
	}

	// Should have: LoadVar(x), LoadNum(42), OpEq = 3 instructions = 12 bytes
	if rh.ConditionLen != 3*compiler.InstrSize {
		t.Errorf("condition length = %d, want %d (3 instructions)", rh.ConditionLen, 3*compiler.InstrSize)
	}

	// Decode and verify opcodes
	code := rs.Instructions[rh.ConditionOff : rh.ConditionOff+rh.ConditionLen]
	ops := decodeOps(code)
	if len(ops) < 3 {
		t.Fatalf("expected 3 opcodes, decoded %d", len(ops))
	}
	if ops[0] != compiler.OpLoadVar {
		t.Errorf("ops[0] = %d, want OpLoadVar(%d)", ops[0], compiler.OpLoadVar)
	}
	if ops[1] != compiler.OpLoadNum {
		t.Errorf("ops[1] = %d, want OpLoadNum(%d)", ops[1], compiler.OpLoadNum)
	}
	if ops[2] != compiler.OpEq {
		t.Errorf("ops[2] = %d, want OpEq(%d)", ops[2], compiler.OpEq)
	}

	// Verify the number in the pool
	var buf [compiler.InstrSize]byte
	copy(buf[:], code[compiler.InstrSize:2*compiler.InstrSize])
	_, _, arg := compiler.DecodeInstr(buf)
	num := rs.Constants.GetNumber(arg)
	if num != 42 {
		t.Errorf("constant = %f, want 42", num)
	}
}

func TestCompileMultipleRules(t *testing.T) {
	src := `
rule Alpha {
    when { a > 1 }
    then DoA {}
}

rule Beta {
    when { b < 2 }
    then DoB {}
}

rule Gamma {
    when { c == 3 }
    then DoC {}
}
`
	rs := compileSource(t, src)

	if len(rs.Rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rs.Rules))
	}

	names := make([]string, len(rs.Rules))
	for i, rh := range rs.Rules {
		names[i] = rs.Constants.GetString(rh.NameIdx)
	}
	want := []string{"Alpha", "Beta", "Gamma"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("rule[%d] = %q, want %q", i, names[i], w)
		}
	}

	// Each condition should be non-empty
	for i, rh := range rs.Rules {
		if rh.ConditionLen == 0 {
			t.Errorf("rule[%d] has empty condition", i)
		}
	}
}

func TestCompileAndOr(t *testing.T) {
	src := `
rule Combined {
    when { a > 1 and b < 2 or c == 3 }
    then Act {}
}
`
	rs := compileSource(t, src)

	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}

	rh := rs.Rules[0]
	code := rs.Instructions[rh.ConditionOff : rh.ConditionOff+rh.ConditionLen]
	ops := decodeOps(code)

	// Verify OpAnd and OpOr are present in the bytecode
	hasAnd := false
	hasOr := false
	hasJumpIfFalse := false
	hasJumpIfTrue := false
	for _, op := range ops {
		if op == compiler.OpAnd {
			hasAnd = true
		}
		if op == compiler.OpOr {
			hasOr = true
		}
		if op == compiler.OpJumpIfFalse {
			hasJumpIfFalse = true
		}
		if op == compiler.OpJumpIfTrue {
			hasJumpIfTrue = true
		}
	}
	if !hasAnd {
		t.Error("expected OpAnd in bytecode for 'and' expression")
	}
	if !hasOr {
		t.Error("expected OpOr in bytecode for 'or' expression")
	}
	if !hasJumpIfFalse {
		t.Error("expected OpJumpIfFalse for DSL short-circuit AND")
	}
	if !hasJumpIfTrue {
		t.Error("expected OpJumpIfTrue for DSL short-circuit OR")
	}
}

func TestCompileConst(t *testing.T) {
	src := `
const THRESHOLD = 100
const NAME = "admin"

rule ConstCheck {
    when { x >= THRESHOLD and y == NAME }
    then Allow {}
}
`
	rs := compileSource(t, src)

	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}

	rh := rs.Rules[0]
	code := rs.Instructions[rh.ConditionOff : rh.ConditionOff+rh.ConditionLen]
	ops := decodeOps(code)

	// The const THRESHOLD=100 should be inlined as OpLoadNum
	// The const NAME="admin" should be inlined as OpLoadStr
	hasLoadNum := false
	hasLoadStr := false
	for _, op := range ops {
		if op == compiler.OpLoadNum {
			hasLoadNum = true
		}
		if op == compiler.OpLoadStr {
			hasLoadStr = true
		}
	}
	if !hasLoadNum {
		t.Error("expected OpLoadNum for inlined numeric constant THRESHOLD")
	}
	if !hasLoadStr {
		t.Error("expected OpLoadStr for inlined string constant NAME")
	}

	// Verify the number 100 is in the pool
	found := false
	for i := 0; i < rs.Constants.NumberCount(); i++ {
		if rs.Constants.GetNumber(uint16(i)) == 100 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 100 in number pool from THRESHOLD constant")
	}

	// Verify "admin" is in the string pool
	foundStr := false
	for _, s := range rs.Constants.Strings() {
		if s == "admin" {
			foundStr = true
			break
		}
	}
	if !foundStr {
		t.Error("expected 'admin' in string pool from NAME constant")
	}
}

func TestCompileListUsesPairEncoding(t *testing.T) {
	src := `
rule HasRole {
    when { role in ["admin", "mod"] }
    then Allow {}
}
`
	rs := compileSource(t, src)
	rh := rs.Rules[0]
	code := rs.Instructions[rh.ConditionOff : rh.ConditionOff+rh.ConditionLen]

	foundListHead := false
	foundListTail := false
	for i := 0; i+compiler.InstrSize <= len(code); i += compiler.InstrSize {
		var buf [compiler.InstrSize]byte
		copy(buf[:], code[i:i+compiler.InstrSize])
		op, flags, _ := compiler.DecodeInstr(buf)
		if op == compiler.OpLoadNull && flags == intern.TypeList {
			foundListHead = true
		}
		if op == compiler.OpLoadNull && flags == 0xFF {
			foundListTail = true
		}
	}

	if !foundListHead || !foundListTail {
		t.Fatalf("expected list pair encoding, got head=%v tail=%v", foundListHead, foundListTail)
	}
}

func TestCompileRuleGovernanceMetadata(t *testing.T) {
	src := `
rule EnhancedRiskCheck priority 1 {
    kill_switch
    requires BasicRiskCheck
    requires prior_hold
    when segment high_risk {
        tx.amount > 5000
    }
    then Hold {}
    rollout 20
}
`
	rs := compileSource(t, src)
	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}

	rh := rs.Rules[0]
	if !rh.KillSwitch {
		t.Fatal("expected KillSwitch to be set")
	}
	if !rh.HasSegment {
		t.Fatal("expected HasSegment to be set")
	}
	if got := rs.Constants.GetString(rh.SegmentNameIdx); got != "high_risk" {
		t.Fatalf("segment name = %q, want high_risk", got)
	}
	if rh.Rollout != 20 {
		t.Fatalf("rollout = %d, want 20", rh.Rollout)
	}
	if rh.PrereqLen != 2 {
		t.Fatalf("prereq len = %d, want 2", rh.PrereqLen)
	}
	if len(rs.Prereqs) != 2 {
		t.Fatalf("prereq table len = %d, want 2", len(rs.Prereqs))
	}

	gotPrereqs := []string{
		rs.Constants.GetString(rs.Prereqs[rh.PrereqOff]),
		rs.Constants.GetString(rs.Prereqs[rh.PrereqOff+1]),
	}
	wantPrereqs := []string{"BasicRiskCheck", "prior_hold"}
	for i, want := range wantPrereqs {
		if gotPrereqs[i] != want {
			t.Fatalf("prereq[%d] = %q, want %q", i, gotPrereqs[i], want)
		}
	}
}

func TestCompileTestdataFiles(t *testing.T) {
	testcases := []struct {
		file      string
		wantRules int
	}{
		{"../testdata/pricing.arb", 3},
		{"../testdata/fraud.arb", 6},
		{"../testdata/moderation.arb", 5},
	}

	for _, tc := range testcases {
		t.Run(tc.file, func(t *testing.T) {
			src, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}

			lang, err := arbiter.GetLanguage()
			if err != nil {
				t.Fatalf("GetLanguage: %v", err)
			}

			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			root := tree.RootNode()
			rs, err := compiler.CompileCST(root, src, lang)
			if err != nil {
				t.Fatalf("CompileCST: %v", err)
			}

			if len(rs.Rules) != tc.wantRules {
				t.Errorf("got %d rules, want %d", len(rs.Rules), tc.wantRules)
			}

			// Every rule should have non-empty condition bytecode
			for i, rh := range rs.Rules {
				if rh.ConditionLen == 0 {
					t.Errorf("rule[%d] (%s) has empty condition", i, rs.Constants.GetString(rh.NameIdx))
				}
			}

			// Instructions should be non-empty
			if len(rs.Instructions) == 0 {
				t.Error("instructions are empty")
			}

			// Constants pool should have entries
			if rs.Constants.StringCount() == 0 {
				t.Error("string pool is empty")
			}

			t.Logf("compiled %s: %d rules, %d bytes code, %d strings, %d numbers",
				tc.file, len(rs.Rules), len(rs.Instructions),
				rs.Constants.StringCount(), rs.Constants.NumberCount())
		})
	}
}

// decodeOps extracts opcodes from bytecode for test assertions.
func decodeOps(code []byte) []compiler.OpCode {
	var ops []compiler.OpCode
	for i := 0; i+compiler.InstrSize <= len(code); i += compiler.InstrSize {
		var buf [compiler.InstrSize]byte
		copy(buf[:], code[i:i+compiler.InstrSize])
		op, _, _ := compiler.DecodeInstr(buf)
		ops = append(ops, op)
	}
	return ops
}
