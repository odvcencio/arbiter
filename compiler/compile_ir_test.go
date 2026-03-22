package compiler_test

import (
	"testing"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/compiler"
	"github.com/odvcencio/arbiter/ir"
)

func TestCompileIRDirect(t *testing.T) {
	parsed, err := arbiter.ParseSource([]byte(`
const MIN_AGE = 18

rule AgeCheck {
    when {
        let floor = MIN_AGE
        user.age >= floor
    }
    then Approve {
        reason: "adult"
    }
}
`))
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}

	program, err := ir.Lower(parsed.Root, parsed.Source, parsed.Lang)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}

	rs, err := compiler.CompileIR(program)
	if err != nil {
		t.Fatalf("CompileIR: %v", err)
	}

	if len(rs.Rules) != 1 {
		t.Fatalf("len(Rules) = %d, want 1", len(rs.Rules))
	}
	rh := rs.Rules[0]
	if rh.ConditionLen == 0 {
		t.Fatal("condition bytecode is empty")
	}

	ops := decodeOps(rs.Instructions[rh.ConditionOff : rh.ConditionOff+rh.ConditionLen])
	var (
		hasSetLocal bool
		hasLoadVar  bool
		hasGte      bool
	)
	for _, op := range ops {
		if op == compiler.OpSetLocal {
			hasSetLocal = true
		}
		if op == compiler.OpLoadVar {
			hasLoadVar = true
		}
		if op == compiler.OpGte {
			hasGte = true
		}
	}
	if !hasSetLocal {
		t.Fatal("expected let binding to emit OpSetLocal")
	}
	if !hasLoadVar {
		t.Fatal("expected variable/local loads to emit OpLoadVar")
	}
	if !hasGte {
		t.Fatal("expected comparison to emit OpGte")
	}
}
