package gameai

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// MemorySlots is the number of keys one Memory holds.
const MemorySlots = 16

// Never is the tick Memory.Last reports for a key that has not fired. It is far
// enough in the past that any cooldown test such as
// `tick - mem.last.Dash >= 90` passes, and small enough that tick - Never stays
// an exact integer (it is -2^40, so it also fits the integer numeric profile).
const Never = -(1 << 40)

// Memory is one agent's decision memory across ticks: for up to MemorySlots
// keys (rule names, action kinds, anything the game chooses), the tick the key
// last fired and how many times it fired. Rules read it through a bound
// Struct as mem.last.<key> and mem.count.<key>.
//
// Memory is a fixed-size value. Copying it takes a snapshot and assigning the
// copy back restores it, so it rolls back with the rest of the simulation
// state. MarshalBinary gives a deterministic encoding for snapshots sent over
// the network. Keys are kept sorted, and when Memory is full a new key evicts
// the key that fired longest ago (ties: the smaller key), so every peer
// evicts the same key.
type Memory struct {
	n     uint8
	keys  [MemorySlots]string
	last  [MemorySlots]int64
	count [MemorySlots]uint32
}

// Note records that key fired at tick.
func (m *Memory) Note(key string, tick int64) {
	i, found := m.find(key)
	if found {
		m.last[i] = tick
		if m.count[i] < ^uint32(0) {
			m.count[i]++
		}
		return
	}
	if int(m.n) == MemorySlots {
		m.remove(m.oldest())
		i, _ = m.find(key)
	}
	copy(m.keys[i+1:m.n+1], m.keys[i:m.n])
	copy(m.last[i+1:m.n+1], m.last[i:m.n])
	copy(m.count[i+1:m.n+1], m.count[i:m.n])
	m.keys[i], m.last[i], m.count[i] = key, tick, 1
	m.n++
}

// Last returns the tick key last fired, or Never.
func (m *Memory) Last(key string) int64 {
	if i, ok := m.find(key); ok {
		return m.last[i]
	}
	return Never
}

// Count returns how many times key fired since it entered the memory.
func (m *Memory) Count(key string) uint32 {
	if i, ok := m.find(key); ok {
		return m.count[i]
	}
	return 0
}

// Since returns the ticks elapsed at tick since key last fired, or tick-Never.
func (m *Memory) Since(key string, tick int64) int64 { return tick - m.Last(key) }

// Forget removes key.
func (m *Memory) Forget(key string) {
	if i, ok := m.find(key); ok {
		m.remove(i)
	}
}

// Reset removes every key.
func (m *Memory) Reset() { *m = Memory{} }

// Len returns the number of keys held.
func (m *Memory) Len() int { return int(m.n) }

// Keys returns the keys held, sorted.
func (m *Memory) Keys() []string { return append([]string(nil), m.keys[:m.n]...) }

// find returns the index of key, or the index where it would be inserted.
func (m *Memory) find(key string) (int, bool) {
	lo, hi := 0, int(m.n)
	for lo < hi {
		mid := (lo + hi) / 2
		if m.keys[mid] < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, lo < int(m.n) && m.keys[lo] == key
}

// oldest returns the index of the key that fired longest ago. Keys are sorted,
// so the first one found with the smallest tick is the smallest such key.
func (m *Memory) oldest() int {
	best := 0
	for i := 1; i < int(m.n); i++ {
		if m.last[i] < m.last[best] {
			best = i
		}
	}
	return best
}

func (m *Memory) remove(i int) {
	n := int(m.n)
	copy(m.keys[i:n-1], m.keys[i+1:n])
	copy(m.last[i:n-1], m.last[i+1:n])
	copy(m.count[i:n-1], m.count[i+1:n])
	m.keys[n-1], m.last[n-1], m.count[n-1] = "", 0, 0
	m.n--
}

const memoryEncodingVersion = 1

// AppendBinary appends the deterministic encoding of m to b: a version byte,
// the key count, then per key (in sorted order) its length and bytes, the last
// tick as a zigzag varint and the count as a varint.
func (m *Memory) AppendBinary(b []byte) ([]byte, error) {
	b = append(b, memoryEncodingVersion, m.n)
	for i := 0; i < int(m.n); i++ {
		b = binary.AppendUvarint(b, uint64(len(m.keys[i])))
		b = append(b, m.keys[i]...)
		b = binary.AppendVarint(b, m.last[i])
		b = binary.AppendUvarint(b, uint64(m.count[i]))
	}
	return b, nil
}

// MarshalBinary implements encoding.BinaryMarshaler.
func (m *Memory) MarshalBinary() ([]byte, error) { return m.AppendBinary(nil) }

// UnmarshalBinary implements encoding.BinaryUnmarshaler. It rejects encodings
// that are not sorted or hold more than MemorySlots keys.
func (m *Memory) UnmarshalBinary(b []byte) error {
	var out Memory
	if len(b) < 2 || b[0] != memoryEncodingVersion {
		return errors.New("gameai: memory: unknown encoding")
	}
	n := int(b[1])
	if n > MemorySlots {
		return fmt.Errorf("gameai: memory: %d keys, at most %d", n, MemorySlots)
	}
	b = b[2:]
	for i := 0; i < n; i++ {
		size, k := binary.Uvarint(b)
		if k <= 0 || size > uint64(len(b)-k) {
			return errors.New("gameai: memory: truncated key")
		}
		key := string(b[k : k+int(size)])
		b = b[k+int(size):]
		last, k := binary.Varint(b)
		if k <= 0 {
			return errors.New("gameai: memory: truncated tick")
		}
		b = b[k:]
		count, k := binary.Uvarint(b)
		if k <= 0 || count > uint64(^uint32(0)) {
			return errors.New("gameai: memory: bad count")
		}
		b = b[k:]
		if i > 0 && key <= out.keys[i-1] {
			return errors.New("gameai: memory: keys not sorted")
		}
		out.keys[i], out.last[i], out.count[i] = key, last, uint32(count)
	}
	if len(b) != 0 {
		return errors.New("gameai: memory: trailing bytes")
	}
	out.n = uint8(n)
	*m = out
	return nil
}
