// Package bundle provides binary serialization and obfuscation for compiled
// Arbiter rulesets. Use this to ship pre-compiled bundles to browsers and edge
// runtimes without exposing .arb source, governance parameters, or rule internals.
//
// The binary codec itself lives in package wire, which does not import the
// .arb parser. Programs that only load bundles should import wire directly.
package bundle

import (
	"m31labs.dev/arbiter/bundle/wire"
	"m31labs.dev/arbiter/compiler"
)

// ObfuscateOptions controls what gets stripped or hashed in the serialized bundle.
type ObfuscateOptions = wire.ObfuscateOptions

// Marshal serializes a CompiledRuleset to a binary blob.
func Marshal(rs *compiler.CompiledRuleset) ([]byte, error) { return wire.Marshal(rs) }

// MarshalObfuscated serializes a CompiledRuleset with obfuscation applied.
func MarshalObfuscated(rs *compiler.CompiledRuleset, opts ObfuscateOptions) ([]byte, error) {
	return wire.MarshalObfuscated(rs, opts)
}

// Unmarshal deserializes a binary blob into a CompiledRuleset.
func Unmarshal(data []byte) (*compiler.CompiledRuleset, error) { return wire.Unmarshal(data) }
