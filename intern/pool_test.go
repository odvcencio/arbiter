// intern/pool_test.go
package intern

import "testing"

func TestStringInterning(t *testing.T) {
	p := NewPool()
	idx1 := p.String("hello")
	idx2 := p.String("world")
	idx3 := p.String("hello") // duplicate

	if idx1 != idx3 {
		t.Errorf("same string got different indices: %d vs %d", idx1, idx3)
	}
	if idx1 == idx2 {
		t.Errorf("different strings got same index")
	}
	if p.GetString(idx1) != "hello" {
		t.Errorf("GetString: got %q, want %q", p.GetString(idx1), "hello")
	}
}

func TestNumberInterning(t *testing.T) {
	p := NewPool()
	idx1 := p.Number(3.14)
	idx2 := p.Number(2.71)
	idx3 := p.Number(3.14)

	if idx1 != idx3 {
		t.Errorf("same number got different indices: %d vs %d", idx1, idx3)
	}
	if idx1 == idx2 {
		t.Errorf("different numbers got same index")
	}
	if p.GetNumber(idx1) != 3.14 {
		t.Errorf("GetNumber: got %f, want 3.14", p.GetNumber(idx1))
	}
}

func TestListStorage(t *testing.T) {
	p := NewPool()
	items := []PoolValue{
		{Typ: TypeString, Str: p.String("a")},
		{Typ: TypeString, Str: p.String("b")},
		{Typ: TypeString, Str: p.String("c")},
	}
	idx, length := p.List(items)

	got := p.GetList(idx, length)
	if len(got) != 3 {
		t.Fatalf("list length: got %d, want 3", len(got))
	}
	if p.GetString(got[0].Str) != "a" {
		t.Errorf("first element: got %q, want %q", p.GetString(got[0].Str), "a")
	}
}

func TestPoolCounts(t *testing.T) {
	p := NewPool()
	p.String("a")
	p.String("b")
	p.String("a") // dup
	p.Number(1)
	p.Number(1) // dup

	if p.StringCount() != 2 {
		t.Errorf("string count: got %d, want 2", p.StringCount())
	}
	if p.NumberCount() != 1 {
		t.Errorf("number count: got %d, want 1", p.NumberCount())
	}
}
