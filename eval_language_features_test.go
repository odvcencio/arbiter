package arbiter

import "testing"

func TestAggregateSum(t *testing.T) {
	rs, err := Compile([]byte(`
rule T {
	when { sum(item.price for item in cart.items) > 100 }
	then Match { total: sum(item.price for item in cart.items) }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{
		"cart": map[string]any{
			"items": []any{
				map[string]any{"price": 50.0},
				map[string]any{"price": 60.0},
			},
		},
	}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if got := matched[0].Params["total"]; got != 110.0 {
		t.Fatalf("total: got %v, want 110", got)
	}
}

func TestAggregateCount(t *testing.T) {
	rs, err := Compile([]byte(`
rule T {
	when { count(item in cart.items) > 1 }
	then Match {}
}
`))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{
		"cart": map[string]any{
			"items": []any{
				map[string]any{"name": "a"},
				map[string]any{"name": "b"},
			},
		},
	}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatal("expected match: count=2 > 1")
	}
}

func TestAggregateAvg(t *testing.T) {
	rs, err := Compile([]byte(`
rule T {
	when { avg(score.value for score in scores) > 7 }
	then Match {}
}
`))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{
		"scores": []any{
			map[string]any{"value": 8.0},
			map[string]any{"value": 9.0},
			map[string]any{"value": 6.0},
		},
	}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatal("expected match: avg=7.67 > 7")
	}
}

func TestLetBinding(t *testing.T) {
	rs, err := Compile([]byte(`
rule T {
	when {
		let total = income.wages + income.interest
		total > 50000
	}
	then Match { amount: total }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	dc := DataFromMap(map[string]any{
		"income": map[string]any{
			"wages":    40000.0,
			"interest": 15000.0,
		},
	}, rs)
	matched, err := Eval(rs, dc)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatal("expected match: total=55000 > 50000")
	}
	if got := matched[0].Params["amount"]; got != 55000.0 {
		t.Fatalf("amount: got %v, want 55000", got)
	}
}
