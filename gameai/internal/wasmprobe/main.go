// Command wasmprobe is the smallest useful program built on package gameai: it
// loads a bundle, binds a view struct and makes one decision. TestWasmSize
// builds it for js/wasm to measure what the runtime adds to a browser binary.
package main

import (
	"m31labs.dev/arbiter/examples/gameai/arena"
	"m31labs.dev/arbiter/gameai"
)

func main() {
	prog, err := gameai.Load(arena.Bundle)
	if err != nil {
		panic(err)
	}
	brain, err := gameai.NewBrain[arena.View](prog, arena.Decisions)
	if err != nil {
		panic(err)
	}
	intent, err := brain.Decision("intent")
	if err != nil {
		panic(err)
	}
	var v arena.View
	v.Self.HasOrb, v.Self.Pressure = true, 2
	c, err := intent.Decide(&v, 0)
	if err != nil {
		panic(err)
	}
	println(c.Rule(), c.Text("kind"), int(c.Score()))
}
