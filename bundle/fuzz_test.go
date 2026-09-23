package bundle_test

import (
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle"
)

// FuzzBundleUnmarshal fuzzes the bundle binary decoder directly. Unlike
// FuzzCompile/FuzzParse/FuzzEval (source text), this exercises a different
// attack surface: a byte-for-byte malformed or truncated binary blob handed
// to Unmarshal by whoever loads a pre-compiled bundle (a CDN-served bundle
// for a WASM/edge runtime, a bundle read from disk or a network call), which
// never goes through the .arb parser at all.
func FuzzBundleUnmarshal(f *testing.F) {
	// Seed with a real, valid bundle produced by Marshal, so the fuzzer
	// starts from well-formed input and mutates it (truncation, bit flips,
	// field corruption) rather than only ever hitting the "too short to
	// have a header" early-return path.
	prog, err := arbiter.Compile([]byte(`
rule FreeShipping {
	when { order.total >= 100 }
	then ApplyShipping { cost: 0, method: "free" }
}

rule StandardShipping {
	when { order.total < 100 }
	then ApplyShipping { cost: 5.99, method: "standard" }
}
`))
	if err != nil {
		f.Fatalf("compile seed program: %v", err)
	}
	seed, err := bundle.Marshal(prog.Ruleset)
	if err != nil {
		f.Fatalf("marshal seed bundle: %v", err)
	}
	f.Add(seed)

	// Also seed with an obfuscated bundle, which exercises the string/rule
	// name hashing path's binary layout.
	obfuscated, err := bundle.MarshalObfuscated(prog.Ruleset, bundle.ObfuscateOptions{
		HashRuleNames:       true,
		HashSegmentNames:    true,
		StripRolloutDetails: true,
		StripPrereqs:        true,
	})
	if err != nil {
		f.Fatalf("marshal obfuscated seed bundle: %v", err)
	}
	f.Add(obfuscated)

	f.Add([]byte(""))
	f.Add([]byte("ARB1"))
	f.Add([]byte{0x00, 0x01, 0x02, 0x03})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic on any input, valid or corrupted.
		bundle.Unmarshal(data)
	})
}
