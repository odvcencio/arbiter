// vm/datacontext_test.go
package vm

import "testing"

func TestMapContext(t *testing.T) {
	pool := &StringPool{strs: []string{"name", "age", "missing", "alice"}}
	dc := DataFromMap(map[string]any{
		"name": "alice",
		"age":  30.0,
	}, pool)

	v := dc.Get("name")
	if v.Typ != TypeString {
		t.Errorf("name type: got %d, want %d", v.Typ, TypeString)
	}

	v = dc.Get("age")
	if v.Typ != TypeNumber || v.Num != 30.0 {
		t.Errorf("age: got %+v, want number 30", v)
	}

	v = dc.Get("missing")
	if !v.IsNull() {
		t.Error("missing key should return null")
	}
}

func TestNestedMapContext(t *testing.T) {
	pool := &StringPool{strs: []string{"user.name", "user.age", "alice"}}
	dc := DataFromMap(map[string]any{
		"user": map[string]any{
			"name": "alice",
			"age":  25.0,
		},
	}, pool)

	v := dc.Get("user.name")
	if v.Typ != TypeString {
		t.Errorf("user.name type: got %d, want %d", v.Typ, TypeString)
	}

	v = dc.Get("user.age")
	if v.Num != 25.0 {
		t.Errorf("user.age: got %f, want 25", v.Num)
	}
}

func TestJSONContext(t *testing.T) {
	pool := &StringPool{strs: []string{"name", "bob"}}
	dc, err := DataFromJSON(`{"name": "bob"}`, pool)
	if err != nil {
		t.Fatal(err)
	}
	v := dc.Get("name")
	if v.Typ != TypeString {
		t.Errorf("name type: got %d, want %d", v.Typ, TypeString)
	}
}
