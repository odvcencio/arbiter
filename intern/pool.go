// intern/pool.go
package intern

// TypeTag identifies the type of a pooled value.
const (
	TypeNull   uint8 = 0
	TypeBool   uint8 = 1
	TypeNumber uint8 = 2
	TypeString uint8 = 3
	TypeList   uint8 = 4
)

// PoolValue is the VM's value representation used in lists.
type PoolValue struct {
	Typ     uint8
	Num     float64
	Str     uint16 // constant pool string index
	Bool    bool
	ListIdx uint16
	ListLen uint16
}

// Pool stores deduplicated constants for a compiled ruleset.
type Pool struct {
	strings  []string
	numbers  []float64
	lists    []PoolValue
	strIndex map[string]uint16
	numIndex map[float64]uint16
}

// NewPool creates an empty constant pool.
func NewPool() *Pool {
	return &Pool{
		strIndex: make(map[string]uint16),
		numIndex: make(map[float64]uint16),
	}
}

// String interns a string and returns its index.
func (p *Pool) String(s string) uint16 {
	if idx, ok := p.strIndex[s]; ok {
		return idx
	}
	idx := uint16(len(p.strings))
	p.strings = append(p.strings, s)
	p.strIndex[s] = idx
	return idx
}

// Number interns a number and returns its index.
func (p *Pool) Number(n float64) uint16 {
	if idx, ok := p.numIndex[n]; ok {
		return idx
	}
	idx := uint16(len(p.numbers))
	p.numbers = append(p.numbers, n)
	p.numIndex[n] = idx
	return idx
}

// List stores a list of values contiguously and returns (start index, length).
func (p *Pool) List(items []PoolValue) (uint16, uint16) {
	start := uint16(len(p.lists))
	p.lists = append(p.lists, items...)
	return start, uint16(len(items))
}

// GetString returns the string at the given pool index.
func (p *Pool) GetString(idx uint16) string {
	if int(idx) >= len(p.strings) {
		return ""
	}
	return p.strings[idx]
}

// GetNumber returns the number at the given pool index.
func (p *Pool) GetNumber(idx uint16) float64 {
	if int(idx) >= len(p.numbers) {
		return 0
	}
	return p.numbers[idx]
}

// GetList returns a slice of pool values from the flat list storage.
func (p *Pool) GetList(idx, length uint16) []PoolValue {
	start := int(idx)
	end := start + int(length)
	if start > len(p.lists) || end > len(p.lists) {
		return nil
	}
	return p.lists[start:end]
}

// StringCount returns the number of unique interned strings.
func (p *Pool) StringCount() int { return len(p.strings) }

// NumberCount returns the number of unique interned numbers.
func (p *Pool) NumberCount() int { return len(p.numbers) }

// Strings returns all interned strings (for serialization).
func (p *Pool) Strings() []string { return p.strings }

// Numbers returns all interned numbers (for serialization).
func (p *Pool) Numbers() []float64 { return p.numbers }

// Lists returns the flat list storage (for serialization).
func (p *Pool) Lists() []PoolValue { return p.lists }
