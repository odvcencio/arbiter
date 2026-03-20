package arbiter_test

import (
	"testing"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/expert"
)

func TestPipelineExampleCompiles(t *testing.T) {
	full, err := arbiter.CompileFullFile("examples/pipeline/sales.arb")
	if err != nil {
		t.Fatalf("CompileFullFile: %v", err)
	}
	if len(full.Arbiters) != 1 {
		t.Fatalf("expected 1 arbiter declaration, got %+v", full.Arbiters)
	}
	if full.Arbiters[0].Name != "sales_pipeline" {
		t.Fatalf("unexpected arbiter declaration: %+v", full.Arbiters[0])
	}
	program, err := expert.CompileFile("examples/pipeline/sales.arb")
	if err != nil {
		t.Fatalf("expert.CompileFile: %v", err)
	}
	if len(program.Rules()) == 0 {
		t.Fatal("expected pipeline example to include compiled expert rules")
	}
}
