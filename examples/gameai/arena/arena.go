// Package arena is a toy team game that shows how to drive CPU players with
// Arbiter. Two teams of bots chase one orb and try to carry it into the other
// goal. The decision logic lives in arena.arb; arena.arbb is its compiled
// bundle, which is what a game ships and what a browser client loads.
//
// Rebuild the bundle after editing arena.arb:
//
//	go test ./examples/gameai/arena -run TestBundleIsCurrent -update
package arena

import _ "embed"

// Source is the decision logic in .arb form.
//
//go:embed arena.arb
var Source []byte

// Bundle is Source compiled to a binary bundle with readable rule names.
//
//go:embed arena.arbb
var Bundle []byte
