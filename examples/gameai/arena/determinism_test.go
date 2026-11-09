package arena_test

import (
	"testing"
)

// wantMatchDigest is the digest of a 1,800-tick match (one minute at 30 Hz,
// 118,800 decisions) with seed 7. It must be the same on every target.
const wantMatchDigest = 0x05cb978d409fbb00

func TestMatchDigest(t *testing.T) {
	m := newMatch(t, 7)
	for i := 0; i < 1800; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("match digest %#016x score %v", m.Digest, m.Score)
	if m.Digest != wantMatchDigest {
		t.Fatalf("match digest %#016x, want %#016x", m.Digest, uint64(wantMatchDigest))
	}
}

// TestSnapshotRestoreReplaysExactly snapshots the state mid-match, plays on,
// restores, and plays the same ticks again: per-agent memory (cooldowns) and
// every decision must replay identically.
func TestSnapshotRestoreReplaysExactly(t *testing.T) {
	m := newMatch(t, 21)
	for i := 0; i < 400; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	snap := m.State
	for i := 0; i < 400; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	first := m.State
	m.State = snap
	for i := 0; i < 400; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	if m.State != first {
		t.Fatalf("replay after restore diverged: digest %#x vs %#x", m.Digest, first.Digest)
	}
	if m.Digest == snap.Digest {
		t.Fatal("no decisions were made after the snapshot")
	}
}
