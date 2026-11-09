package gameai

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"m31labs.dev/arbiter/vm"
)

// Struct is a data context that reads a Go struct's fields directly. It is
// bound to one Program: Bind resolves every variable name the program can read
// to a field once, and evaluation then reads fields by string-pool index, with
// no map and no allocation.
//
// Fields are mapped by `arb` struct tags, the convention arbiter.DataFromStruct
// uses. A tag on a struct-typed field (or pointer to struct) names a prefix for
// the fields inside it, so
//
//	type Agent struct {
//		Self struct {
//			Stamina float64 `arb:"stamina"`
//		} `arb:"self"`
//	}
//
// binds self.stamina. A tag may also contain dots (`arb:"self.stamina"`).
// Untagged embedded structs are flattened. Supported field types are bool,
// signed and unsigned integers, floats, strings (and pointers to them; a nil
// pointer reads as null), and Memory, which exposes mem.last.<key> and
// mem.count.<key> under its tag.
//
// A string field reads without allocating when its value is one of the
// program's string constants (any value a rule compares it with), or a value
// the context has read before (up to 256 distinct values). Integers are
// converted to float64; keep them within 2^53.
//
// A Struct is not safe for concurrent use. Bind one per goroutine.
type Struct[T any] struct {
	bound
}

// bound is the non-generic part of Struct. Evaluation calls it through the
// vm.DataContext interface, so keeping it free of type parameters keeps those
// calls direct.
type bound struct {
	prog   *Program
	lay    *layout
	byIdx  []accessor
	strIdx map[string]uint16
	boxed  map[string]vm.Value // runtime strings seen before, already boxed
	cur    reflect.Value
	base   unsafe.Pointer
	set    bool
}

type fieldKind uint8

const (
	kindNone fieldKind = iota
	kindBool
	kindInt
	kindUint
	kindFloat
	kindString
	kindMemLast
	kindMemCount
)

type accessor struct {
	kind  fieldKind
	ptr   bool // the leaf field is a pointer
	index []int
	key   string // memory entry key
	// direct accessors have no pointer between T and the leaf, so the leaf
	// is at a fixed offset from the start of T and is read without reflect.
	direct bool
	off    uintptr
	gk     reflect.Kind
}

type layout struct {
	leaves      map[string]accessor
	memories    []memoryField
	unsupported map[string]string // path -> Go type
}

type memoryField struct {
	prefix string // "mem." for `arb:"mem"`
	index  []int
	ptr    bool
	direct bool
	off    uintptr
}

var (
	layoutCache sync.Map // reflect.Type -> *layout
	memoryType  = reflect.TypeOf(Memory{})
)

// Bind binds struct type T to program p. It fails if T is not a struct.
func Bind[T any](p *Program) (*Struct[T], error) {
	if p == nil {
		return nil, fmt.Errorf("gameai: bind: nil program")
	}
	t := reflect.TypeOf((*T)(nil)).Elem()
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("gameai: bind: %s is not a struct", t)
	}
	lay := layoutOf(t)
	strs := p.rs.Constants.Strings()
	s := &Struct[T]{bound{
		prog:   p,
		lay:    lay,
		byIdx:  make([]accessor, len(strs)),
		strIdx: make(map[string]uint16, len(strs)),
	}}
	for i, str := range strs {
		if _, dup := s.strIdx[str]; !dup {
			s.strIdx[str] = uint16(i)
		}
		s.byIdx[i] = lay.resolve(str)
	}
	return s, nil
}

// Set points the context at v and returns it as a data context to pass to
// Decider.Decide or Decider.Eval. The context reads v's fields during
// evaluation, so v must not change until the evaluation returns. A nil v
// makes every variable read as null.
func (s *Struct[T]) Set(v *T) vm.DataContext {
	if v == nil {
		s.cur, s.base, s.set = reflect.Value{}, nil, false
		return &s.bound
	}
	s.cur, s.base, s.set = reflect.ValueOf(v).Elem(), unsafe.Pointer(v), true
	return &s.bound
}

// Get implements vm.DataContext for the value last passed to Set.
func (s *bound) Get(key string) vm.Value {
	if idx, ok := s.strIdx[key]; ok {
		return s.read(&s.byIdx[idx])
	}
	a := s.lay.resolve(key)
	return s.read(&a)
}

// GetIndexed implements vm.IndexedDataContext.
func (s *bound) GetIndexed(idx uint16, key string) vm.Value {
	if int(idx) < len(s.byIdx) {
		return s.read(&s.byIdx[idx])
	}
	return s.Get(key)
}

// Fields returns the variable names T provides, sorted.
func (s *Struct[T]) Fields() []string {
	names := make([]string, 0, len(s.lay.leaves))
	for name := range s.lay.leaves {
		names = append(names, name)
	}
	for _, m := range s.lay.memories {
		names = append(names, m.prefix+"last.*", m.prefix+"count.*")
	}
	sort.Strings(names)
	return names
}

func (s *bound) boundProgram() *Program { return s.prog }

// provides reports whether the context resolves name.
func (s *bound) provides(name string) (bool, string) {
	if a := s.lay.resolve(name); a.kind != kindNone {
		return true, ""
	}
	if typ, ok := s.lay.unsupported[name]; ok {
		return false, typ
	}
	return false, ""
}

func (s *bound) read(a *accessor) vm.Value {
	if a.kind == kindNone || !s.set {
		return vm.NullVal()
	}
	if a.direct {
		if a.kind == kindString {
			p := unsafe.Add(s.base, a.off)
			if a.ptr {
				if p = *(*unsafe.Pointer)(p); p == nil {
					return vm.NullVal()
				}
			}
			return s.stringValue(*(*string)(p))
		}
		return readDirect(unsafe.Add(s.base, a.off), a)
	}
	f, err := s.cur.FieldByIndexErr(a.index)
	if err != nil {
		return vm.NullVal()
	}
	if a.ptr {
		if f.IsNil() {
			if a.kind == kindMemLast {
				return vm.NumVal(Never)
			}
			if a.kind == kindMemCount {
				return vm.NumVal(0)
			}
			return vm.NullVal()
		}
		f = f.Elem()
	}
	switch a.kind {
	case kindBool:
		return vm.BoolVal(f.Bool())
	case kindInt:
		return vm.NumVal(float64(f.Int()))
	case kindUint:
		return vm.NumVal(float64(f.Uint()))
	case kindFloat:
		return vm.NumVal(f.Float())
	case kindString:
		return s.stringValue(f.String())
	case kindMemLast, kindMemCount:
		// The field's type is Memory (checked by walkStruct), and the value
		// is addressable because cur came from a pointer.
		m := (*Memory)(f.Addr().UnsafePointer())
		if a.kind == kindMemLast {
			return vm.NumVal(float64(m.Last(a.key)))
		}
		return vm.NumVal(float64(m.Count(a.key)))
	}
	return vm.NullVal()
}

// maxBoxedStrings bounds the cache of runtime strings, so a field that holds
// a new string every tick cannot grow it without limit.
const maxBoxedStrings = 256

// stringValue returns str as a pooled string when the program has it as a
// constant, which compares and reads without allocating. Other strings are
// boxed once and cached, up to maxBoxedStrings distinct values.
func (s *bound) stringValue(str string) vm.Value {
	if idx, ok := s.strIdx[str]; ok {
		return vm.StrVal(idx)
	}
	if v, ok := s.boxed[str]; ok {
		return v
	}
	v := vm.Value{Typ: vm.TypeString, Any: str}
	if len(s.boxed) < maxBoxedStrings {
		if s.boxed == nil {
			s.boxed = make(map[string]vm.Value)
		}
		s.boxed[str] = v
	}
	return v
}

// readDirect reads a non-string leaf at p, the address of the field.
func readDirect(p unsafe.Pointer, a *accessor) vm.Value {
	if a.ptr {
		p = *(*unsafe.Pointer)(p)
		if p == nil {
			switch a.kind {
			case kindMemLast:
				return vm.NumVal(Never)
			case kindMemCount:
				return vm.NumVal(0)
			}
			return vm.NullVal()
		}
	}
	switch a.kind {
	case kindMemLast:
		return vm.NumVal(float64((*Memory)(p).Last(a.key)))
	case kindMemCount:
		return vm.NumVal(float64((*Memory)(p).Count(a.key)))
	}
	switch a.gk {
	case reflect.Bool:
		return vm.BoolVal(*(*bool)(p))
	case reflect.Int:
		return vm.NumVal(float64(*(*int)(p)))
	case reflect.Int8:
		return vm.NumVal(float64(*(*int8)(p)))
	case reflect.Int16:
		return vm.NumVal(float64(*(*int16)(p)))
	case reflect.Int32:
		return vm.NumVal(float64(*(*int32)(p)))
	case reflect.Int64:
		return vm.NumVal(float64(*(*int64)(p)))
	case reflect.Uint:
		return vm.NumVal(float64(*(*uint)(p)))
	case reflect.Uint8:
		return vm.NumVal(float64(*(*uint8)(p)))
	case reflect.Uint16:
		return vm.NumVal(float64(*(*uint16)(p)))
	case reflect.Uint32:
		return vm.NumVal(float64(*(*uint32)(p)))
	case reflect.Uint64:
		return vm.NumVal(float64(*(*uint64)(p)))
	case reflect.Uintptr:
		return vm.NumVal(float64(*(*uintptr)(p)))
	case reflect.Float32:
		return vm.NumVal(float64(*(*float32)(p)))
	case reflect.Float64:
		return vm.NumVal(*(*float64)(p))
	}
	return vm.NullVal()
}

func layoutOf(t reflect.Type) *layout {
	if cached, ok := layoutCache.Load(t); ok {
		return cached.(*layout)
	}
	lay := &layout{leaves: map[string]accessor{}, unsupported: map[string]string{}}
	walkStruct(t, "", nil, 0, true, lay, 0)
	cached, _ := layoutCache.LoadOrStore(t, lay)
	return cached.(*layout)
}

const maxBindDepth = 16

func walkStruct(t reflect.Type, prefix string, index []int, base uintptr, direct bool, lay *layout, depth int) {
	if depth > maxBindDepth {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("arb")
		if tag == "-" || (!f.IsExported() && !f.Anonymous) {
			continue
		}
		idx := append(append(make([]int, 0, len(index)+1), index...), i)
		off := base + f.Offset
		ft, isPtr := f.Type, false
		if ft.Kind() == reflect.Pointer {
			ft, isPtr = ft.Elem(), true
		}
		if ft == memoryType {
			if tag != "" {
				lay.memories = append(lay.memories, memoryField{prefix: prefix + tag + ".", index: idx, ptr: isPtr, direct: direct, off: off})
			}
			continue
		}
		if tag == "" {
			if f.Anonymous && ft.Kind() == reflect.Struct {
				walkStruct(ft, prefix, idx, off, direct && !isPtr, lay, depth+1)
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		name := prefix + tag
		kind := leafKind(ft.Kind())
		switch {
		case kind != kindNone:
			if _, dup := lay.leaves[name]; !dup {
				lay.leaves[name] = accessor{kind: kind, ptr: isPtr, index: idx, direct: direct, off: off, gk: ft.Kind()}
			}
		case ft.Kind() == reflect.Struct:
			walkStruct(ft, name+".", idx, off, direct && !isPtr, lay, depth+1)
		default:
			lay.unsupported[name] = f.Type.String()
		}
	}
}

func leafKind(k reflect.Kind) fieldKind {
	switch k {
	case reflect.Bool:
		return kindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return kindInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return kindUint
	case reflect.Float32, reflect.Float64:
		return kindFloat
	case reflect.String:
		return kindString
	}
	return kindNone
}

// resolve maps a variable name to an accessor, or to kindNone.
func (l *layout) resolve(name string) accessor {
	if a, ok := l.leaves[name]; ok {
		return a
	}
	for _, m := range l.memories {
		rest, ok := strings.CutPrefix(name, m.prefix)
		if !ok {
			continue
		}
		if key, ok := strings.CutPrefix(rest, "last."); ok && key != "" {
			return accessor{kind: kindMemLast, ptr: m.ptr, index: m.index, key: key, direct: m.direct, off: m.off}
		}
		if key, ok := strings.CutPrefix(rest, "count."); ok && key != "" {
			return accessor{kind: kindMemCount, ptr: m.ptr, index: m.index, key: key, direct: m.direct, off: m.off}
		}
	}
	return accessor{}
}
