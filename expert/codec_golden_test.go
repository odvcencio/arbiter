package expert_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"testing"

	"m31labs.dev/arbiter/expert"
	"m31labs.dev/arbiter/expert/engine"
)

// goldenPath is per format version. A change to the encoded bytes must come
// with a new programFormatVersion, which selects a new golden file. The update
// mode refuses to overwrite an existing file, so a golden cannot be edited
// while the version stays the same.
func goldenPath(version byte) string {
	return fmt.Sprintf("testdata/program-v%d.golden", version)
}

// TestProgramEncodingIsPinned fails when the encoding of a precompiled program
// changes without a version bump. The golden file holds the metadata JSON (Rule,
// ir.FactSchema and ir.OutcomeSchema) and the SHA-256 of the whole blob, which
// covers the rule and segment wire bundles, so renumbering an opcode fails too.
//
// To bump: increase programFormatVersion in expert/engine/codec.go, then run
// ARBITER_UPDATE_GOLDEN=1 go test ./expert -run TestProgramEncodingIsPinned.
func TestProgramEncodingIsPinned(t *testing.T) {
	p, err := expert.Compile([]byte(precompiledSource))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	blob, err := p.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if len(blob) < 9 || string(blob[:4]) != "ARXP" {
		t.Fatalf("unexpected program header")
	}
	version := blob[4]
	n := binary.LittleEndian.Uint32(blob[5:9])
	meta := blob[9 : 9+n]
	sum := sha256.Sum256(blob)

	var got bytes.Buffer
	got.WriteString("format_version=" + strconv.Itoa(int(version)) + "\n")
	got.WriteString("blob_sha256=" + hex.EncodeToString(sum[:]) + "\n")
	got.Write(meta)
	got.WriteByte('\n')

	path := goldenPath(version)
	if os.Getenv("ARBITER_UPDATE_GOLDEN") == "1" {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%s exists: the golden for a format version is frozen. Bump programFormatVersion to write a new one", path)
		}
		if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden for format version %d: %v (bump without a golden? generate it, see the test comment)", version, err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("the precompiled program encoding changed but programFormatVersion is still %d.\nBump programFormatVersion in expert/engine/codec.go and generate a new golden (see the test comment).\n got: %s\nwant: %s", version, got.Bytes(), want)
	}
}

func TestVersion1ProgramIsRejected(t *testing.T) {
	p, err := expert.Compile([]byte(precompiledSource))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	blob[4] = 1
	if _, err := engine.UnmarshalProgram(blob); err == nil {
		t.Fatal("a version 1 program must be rejected")
	}
}
