// Command wasmprobe is the smallest useful program built on package gameai: it
// loads a bundle and makes one decision. TestWasmSize builds it for js/wasm to
// measure what the runtime adds to a browser binary.
package main

import (
	"m31labs.dev/arbiter/examples/gameai/arena"
	"m31labs.dev/arbiter/gameai"
	"m31labs.dev/arbiter/vm"
)

func main() {
	prog, err := gameai.Load(arena.Bundle)
	if err != nil {
		panic(err)
	}
	ev, err := vm.NewPreparedEvaluator(prog.Ruleset(), prog.Strings(), []string{"intent"})
	if err != nil {
		panic(err)
	}
	matched, err := ev.Eval(vm.DataFromMap(map[string]any{
		"self": map[string]any{"has_orb": true, "pressure": 2.0},
		"orb":  map[string]any{"free": false},
		"team": map[string]any{"score_diff": 0.0},
	}, prog.Strings()))
	if err != nil {
		panic(err)
	}
	println(len(matched))
}
