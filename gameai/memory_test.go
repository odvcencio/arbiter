package gameai_test

import (
	"fmt"
	"reflect"
	"testing"

	"m31labs.dev/arbiter/gameai"
)

func TestMemoryNoteLastCount(t *testing.T) {
	var m gameai.Memory
	if m.Last("Dash") != gameai.Never || m.Count("Dash") != 0 || m.Since("Dash", 5) != 5-gameai.Never {
		t.Fatal("empty memory is not Never/0")
	}
	m.Note("Dash", 10)
	m.Note("Shove", 12)
	m.Note("Dash", 40)
	if m.Last("Dash") != 40 || m.Count("Dash") != 2 || m.Last("Shove") != 12 || m.Len() != 2 {
		t.Fatalf("memory %v: Dash %d/%d Shove %d", m.Keys(), m.Last("Dash"), m.Count("Dash"), m.Last("Shove"))
	}
	snap := m // a copy is a snapshot
	m.Note("Dash", 99)
	m.Forget("Shove")
	if snap.Last("Dash") != 40 || snap.Last("Shove") != 12 {
		t.Fatal("snapshot changed with the original")
	}
	m = snap
	if m.Last("Dash") != 40 || m.Count("Dash") != 2 {
		t.Fatal("restore did not bring the snapshot back")
	}
}

func TestMemoryEvictsOldestDeterministically(t *testing.T) {
	var m gameai.Memory
	for i := 0; i < gameai.MemorySlots; i++ {
		m.Note(fmt.Sprintf("k%02d", i), int64(100+i))
	}
	m.Note("k05", 500) // k00 is now the oldest
	m.Note("new", 600)
	if m.Len() != gameai.MemorySlots || m.Last("k00") != gameai.Never || m.Last("new") != 600 || m.Last("k05") != 500 {
		t.Fatalf("eviction kept %v", m.Keys())
	}
	// Ties go to the smaller key.
	var tie gameai.Memory
	for i := 0; i < gameai.MemorySlots; i++ {
		tie.Note(fmt.Sprintf("t%02d", gameai.MemorySlots-1-i), 7)
	}
	tie.Note("zz", 8)
	if tie.Last("t00") != gameai.Never || tie.Last("t01") != 7 {
		t.Fatalf("tie eviction kept %v", tie.Keys())
	}
}

func TestMemoryBinaryRoundTrip(t *testing.T) {
	var m gameai.Memory
	m.Note("Shove", -3)
	m.Note("Dash", 1<<40)
	m.Note("Dash", 7)
	data, err := m.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var back gameai.Memory
	if err := back.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, back) {
		t.Fatalf("round trip changed memory: %v vs %v", m.Keys(), back.Keys())
	}
	again, _ := back.MarshalBinary()
	if string(again) != string(data) {
		t.Fatal("encoding is not stable")
	}
	for _, bad := range [][]byte{nil, {9, 0}, {1, 17}, append(append([]byte{}, data...), 0), data[:len(data)-1]} {
		var x gameai.Memory
		if err := x.UnmarshalBinary(bad); err == nil {
			t.Errorf("accepted %x", bad)
		}
	}
}

func TestSeedHelpersAreStable(t *testing.T) {
	// Pinned values: any change to these breaks replays and netplay.
	if got := gameai.Seed(7, 1, 2, gameai.Key("tactic")); got != 0xb77239b2b8b92bc6 {
		t.Fatalf("Seed = %#x", got)
	}
	if got := gameai.Key("tactic"); got != 0x1109bde2e62a0011 {
		t.Fatalf("Key = %#x", got)
	}
	if gameai.Unit(0) != 0 || gameai.Unit(^uint64(0)) >= 1 {
		t.Fatal("Unit out of [0,1)")
	}
	r := gameai.Rand{State: 1}
	a := r.Uint64()
	r2 := gameai.Rand{State: 1}
	if r2.Uint64() != a || r.Intn(0) != 0 {
		t.Fatal("Rand is not reproducible")
	}
	for i := 0; i < 1000; i++ {
		if n := r.Intn(6); n < 0 || n >= 6 {
			t.Fatalf("Intn(6) = %d", n)
		}
	}
}
