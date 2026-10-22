package expert_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"strconv"
	"testing"

	"m31labs.dev/arbiter/expert"
)

const goldenMetaPath = "testdata/program-meta.golden"

// TestProgramMetadataEncodingIsPinned fails when the metadata section of a
// precompiled program changes. That section is JSON of Rule, ir.FactSchema and
// ir.OutcomeSchema. If the bytes change, bump programFormatVersion in
// expert/engine/codec.go and regenerate the golden file with
// ARBITER_UPDATE_GOLDEN=1 go test ./expert -run TestProgramMetadataEncodingIsPinned.
func TestProgramMetadataEncodingIsPinned(t *testing.T) {
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
	got := append([]byte("format_version="+strconv.Itoa(int(version))+"\n"), meta...)
	got = append(got, '\n')

	if os.Getenv("ARBITER_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenMetaPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenMetaPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("precompiled program metadata encoding changed.\nIf this is intended, bump programFormatVersion in expert/engine/codec.go and regenerate the golden file (see test comment).\n got: %s\nwant: %s", got, want)
	}
}
