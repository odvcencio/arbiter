package vm

import (
	"testing"

	"m31labs.dev/arbiter/compiler"
	dec "m31labs.dev/arbiter/decimal"
	"m31labs.dev/arbiter/intern"
)

func benchmarkVM() *VM {
	pool := intern.NewPool()
	return newVM(&compiler.CompiledRuleset{Constants: pool}, NewStringPool(pool.Strings()))
}

func BenchmarkVMPushPop(b *testing.B) {
	vm := benchmarkVM()
	value := NumVal(42)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 64; j++ {
			vm.push(value)
		}
		for j := 0; j < 64; j++ {
			_ = vm.pop()
		}
	}
}

func BenchmarkLocalFrames(b *testing.B) {
	cases := []struct {
		name string
		src  []local
	}{
		{name: "empty", src: nil},
		{name: "small", src: []local{{"user", Value{Typ: TypeString, Any: "u_1"}}, {"score", NumVal(720)}, {"country", Value{Typ: TypeString, Any: "US"}}, {"vip", BoolVal(true)}}},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			vm := benchmarkVM()
			vm.locals = tc.src
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				vm.popFrame(vm.pushFrame())
			}
		})
	}
}

func BenchmarkVMValueToAny(b *testing.B) {
	pool := intern.NewPool()
	alphaIdx := pool.String("alpha")
	betaIdx := pool.String("beta")
	listIdx, listLen := pool.List([]intern.PoolValue{
		{Typ: intern.TypeString, Str: alphaIdx},
		{Typ: intern.TypeString, Str: betaIdx},
		{Typ: intern.TypeNumber, Num: 42},
	})
	sp := NewStringPool(pool.Strings())
	vm := newVM(&compiler.CompiledRuleset{Constants: pool}, sp)
	dynamicList := []any{"alpha", "beta", 42.0}
	decimalValue := dec.MustParse("10.25", "USD")

	cases := []struct {
		name  string
		value Value
	}{
		{name: "pooled_string", value: StrVal(alphaIdx)},
		{name: "runtime_string", value: Value{Typ: TypeString, Any: "alpha"}},
		{name: "pooled_list", value: ListVal(listIdx, listLen)},
		{name: "dynamic_list", value: DynListVal(dynamicList)},
		{name: "decimal", value: DecimalVal(decimalValue)},
		{name: "object", value: ObjectVal(map[string]any{"mode": "safe"})},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = vm.valueToAny(tc.value)
			}
		})
	}
}

func BenchmarkVMEvalCondition(b *testing.B) {
	b.Run("numeric_compare", func(b *testing.B) {
		pool := intern.NewPool()
		ageIdx := pool.String("age")
		limitIdx := pool.Number(18)
		var code []byte
		code = compiler.Emit(code, compiler.OpLoadVar, 0, ageIdx)
		code = compiler.Emit(code, compiler.OpLoadNum, 0, limitIdx)
		code = compiler.Emit(code, compiler.OpGt, 0, 0)
		code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)
		rs := &compiler.CompiledRuleset{
			Constants:    pool,
			Instructions: code,
		}
		sp := NewStringPool(pool.Strings())
		vm := newVM(rs, sp)
		dc := DataFromMap(map[string]any{"age": 25.0}, sp)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			vm.sp = 0
			vm.err = nil
			if !vm.evalCondition(code, 0, uint32(len(code)), dc) {
				b.Fatal("expected condition to match")
			}
		}
	})

	b.Run("dynamic_string_compare", func(b *testing.B) {
		pool := intern.NewPool()
		nameIdx := pool.String("name")
		aliceIdx := pool.String("alice")
		var code []byte
		code = compiler.Emit(code, compiler.OpLoadVar, 0, nameIdx)
		code = compiler.Emit(code, compiler.OpLoadStr, 0, aliceIdx)
		code = compiler.Emit(code, compiler.OpEq, 0, 0)
		code = compiler.Emit(code, compiler.OpRuleMatch, 0, 0)
		rs := &compiler.CompiledRuleset{
			Constants:    pool,
			Instructions: code,
		}
		sp := NewStringPool(pool.Strings())
		vm := newVM(rs, sp)
		dc := DataFromMap(map[string]any{"name": "alice"}, sp)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			vm.sp = 0
			vm.err = nil
			if !vm.evalCondition(code, 0, uint32(len(code)), dc) {
				b.Fatal("expected condition to match")
			}
		}
	})
}
