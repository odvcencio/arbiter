// Package bytecode defines the compiled form of an Arbiter program: the
// instruction encoding, the opcode set, and the CompiledRuleset the VM runs.
//
// It is a leaf package. It does not import the .arb parser, the IR, or
// gotreesitter, so a program that only loads and evaluates precompiled
// bundles (package vm, package bundle/wire, package gameai) stays small and
// builds for js/wasm. Package compiler re-exports every name here through
// aliases, so code written against compiler keeps working unchanged.
package bytecode
