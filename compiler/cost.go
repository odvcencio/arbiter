package compiler

import "m31labs.dev/arbiter/bytecode"

// DefaultCostLoopBound is the assumed iteration count for a quantifier or
// aggregate loop body when statically estimating worst-case evaluation cost —
// collection sizes are not known at compile time. CompiledRuleset.WorstCaseCost
// (defined in package bytecode) uses it.
const DefaultCostLoopBound = bytecode.DefaultCostLoopBound
