//go:build js && wasm

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall/js"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle"
	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/expert"
	"m31labs.dev/arbiter/strategy"
	"m31labs.dev/arbiter/vm"
	"m31labs.dev/arbiter/workflow"
)

func main() {
	// Stateless evaluation
	js.Global().Set("arbiterCompile", js.FuncOf(compile))
	js.Global().Set("arbiterEval", js.FuncOf(eval))
	js.Global().Set("arbiterEvalGoverned", js.FuncOf(evalGoverned))
	js.Global().Set("arbiterEvalStrategy", js.FuncOf(evalStrategy))

	// Bundle loading (pre-compiled, no source needed)
	js.Global().Set("arbiterLoadBundle", js.FuncOf(loadBundle))

	// Expert sessions
	js.Global().Set("arbiterStartSession", js.FuncOf(startSession))
	js.Global().Set("arbiterAssertFact", js.FuncOf(assertFact))
	js.Global().Set("arbiterRetractFact", js.FuncOf(retractFact))
	js.Global().Set("arbiterRunSession", js.FuncOf(runSession))
	js.Global().Set("arbiterCloseSession", js.FuncOf(closeSession))

	// Workflows
	js.Global().Set("arbiterCompileWorkflow", js.FuncOf(compileWorkflow))
	js.Global().Set("arbiterSetSourceFacts", js.FuncOf(setSourceFacts))
	js.Global().Set("arbiterRunWorkflow", js.FuncOf(runWorkflow))

	select {}
}

// --- Compilation ---

var (
	compiled      *arbiter.Program
	expertProg    *expert.Program
	lastSource    []byte
	bundleRuleset *compiler.CompiledRuleset // set by loadBundle
)

func compile(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return jsError("compile requires .arb source string")
	}
	source := []byte(args[0].String())
	prog, err := arbiter.Compile(source)
	if err != nil {
		return jsError(err.Error())
	}
	compiled = prog
	lastSource = source
	// Pre-compile expert program for session support.
	ep, eerr := expert.Compile(source)
	if eerr != nil {
		expertProg = nil // not all bundles have expert rules
	} else {
		expertProg = ep
	}
	return jsResult(map[string]any{
		"rules":      len(compiled.Ruleset.Rules),
		"strategies": compiled.Strategies.Count(),
	})
}

// --- Bundle Loading (pre-compiled, no source exposed) ---

func loadBundle(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return jsError("loadBundle requires a base64-encoded bundle string")
	}
	raw := args[0].String()
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return jsError(fmt.Sprintf("decode bundle: %v", err))
	}
	rs, err := bundle.Unmarshal(data)
	if err != nil {
		return jsError(err.Error())
	}
	bundleRuleset = rs
	// Clear source-compiled state — we're in bundle mode now.
	compiled = nil
	expertProg = nil
	lastSource = nil
	return jsResult(map[string]any{
		"rules": len(rs.Rules),
	})
}

// activeRuleset returns whichever ruleset is loaded (bundle or compiled).
func activeRuleset() *compiler.CompiledRuleset {
	if bundleRuleset != nil {
		return bundleRuleset
	}
	if compiled != nil {
		return compiled.Ruleset
	}
	return nil
}

// --- Stateless Eval ---

func eval(_ js.Value, args []js.Value) any {
	rs := activeRuleset()
	if rs == nil {
		return jsError("no compiled bundle — call arbiterCompile or arbiterLoadBundle first")
	}
	if len(args) < 1 {
		return jsError("eval requires JSON context string")
	}
	prog := compiled
	if bundleRuleset != nil {
		prog = &arbiter.Program{Ruleset: rs}
	}
	dc, err := arbiter.DataFromJSON(args[0].String(), prog)
	if err != nil {
		return jsError(err.Error())
	}
	matched, err := arbiter.Eval(prog, dc)
	if errors.Is(err, arbiter.ErrGovernanceRequired) {
		if bundleRuleset != nil {
			return jsError("bundle requires governance; call arbiterCompile with policy source, then arbiterEvalGoverned")
		}
		return jsError("rule requires governance; call arbiterEvalGoverned")
	}
	if err != nil {
		return jsError(err.Error())
	}
	return jsMatchedRules(matched)
}

func evalGoverned(_ js.Value, args []js.Value) any {
	if compiled == nil {
		return jsError("no compiled bundle — call arbiterCompile first")
	}
	if len(args) < 1 {
		return jsError("evalGoverned requires JSON context string")
	}
	var ctx map[string]any
	if err := json.Unmarshal([]byte(args[0].String()), &ctx); err != nil {
		return jsError(err.Error())
	}
	dc, err := arbiter.DataFromJSON(args[0].String(), compiled)
	if err != nil {
		return jsError(err.Error())
	}
	matched, _, err := arbiter.EvalGoverned(compiled, dc, compiled.Segments, ctx)
	if err != nil {
		return jsError(err.Error())
	}
	return jsMatchedRules(matched)
}

func evalStrategy(_ js.Value, args []js.Value) any {
	if compiled == nil {
		return jsError("no compiled bundle — call arbiterCompile first")
	}
	if len(args) < 2 {
		return jsError("evalStrategy requires strategy name and JSON context string")
	}
	name := args[0].String()
	var ctx map[string]any
	if err := json.Unmarshal([]byte(args[1].String()), &ctx); err != nil {
		return jsError(err.Error())
	}
	result, err := compiled.Strategies.Evaluate(name, ctx)
	if err != nil {
		return jsError(err.Error())
	}
	return jsStrategyResult(result)
}

// --- Expert Sessions ---

var (
	sessions   sync.Map
	sessionSeq atomic.Uint64
)

type sessionHandle struct {
	session *expert.Session
}

func startSession(_ js.Value, args []js.Value) any {
	if compiled == nil {
		return jsError("no compiled bundle — call arbiterCompile first")
	}
	if len(args) < 1 {
		return jsError("startSession requires JSON envelope string")
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(args[0].String()), &envelope); err != nil {
		return jsError(err.Error())
	}

	if expertProg == nil {
		return jsError("bundle has no expert rules")
	}

	var facts []expert.Fact
	if len(args) > 1 {
		if err := json.Unmarshal([]byte(args[1].String()), &facts); err != nil {
			return jsError(fmt.Sprintf("invalid facts: %v", err))
		}
	}

	session := expert.NewSession(expertProg, envelope, facts, expert.Options{})
	id := fmt.Sprintf("session-%d", sessionSeq.Add(1))
	sessions.Store(id, &sessionHandle{session: session})
	return jsResult(map[string]any{"sessionId": id})
}

func assertFact(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return jsError("assertFact requires session ID and JSON fact string")
	}
	handle, ok := sessions.Load(args[0].String())
	if !ok {
		return jsError("session not found")
	}
	var fact expert.Fact
	if err := json.Unmarshal([]byte(args[1].String()), &fact); err != nil {
		return jsError(err.Error())
	}
	if err := handle.(*sessionHandle).session.Assert(fact); err != nil {
		return jsError(err.Error())
	}
	return jsResult(map[string]any{"ok": true})
}

func retractFact(_ js.Value, args []js.Value) any {
	if len(args) < 3 {
		return jsError("retractFact requires session ID, fact type, and fact key")
	}
	handle, ok := sessions.Load(args[0].String())
	if !ok {
		return jsError("session not found")
	}
	if err := handle.(*sessionHandle).session.Retract(args[1].String(), args[2].String()); err != nil {
		return jsError(err.Error())
	}
	return jsResult(map[string]any{"ok": true})
}

func runSession(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return jsError("runSession requires session ID")
	}
	handle, ok := sessions.Load(args[0].String())
	if !ok {
		return jsError("session not found")
	}
	s := handle.(*sessionHandle).session
	result, err := s.Run(context.Background())
	if err != nil {
		return jsError(err.Error())
	}
	return jsExpertResult(result)
}

func closeSession(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return jsError("closeSession requires session ID")
	}
	sessions.Delete(args[0].String())
	return jsResult(map[string]any{"ok": true})
}

// --- Workflows ---

var activeWorkflow *workflow.Workflow

func compileWorkflow(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return jsError("compileWorkflow requires .arb source string")
	}
	w, err := workflow.Compile([]byte(args[0].String()), workflow.Options{})
	if err != nil {
		return jsError(err.Error())
	}
	activeWorkflow = w
	return jsResult(map[string]any{
		"sources": w.ExternalSources(),
	})
}

func setSourceFacts(_ js.Value, args []js.Value) any {
	if activeWorkflow == nil {
		return jsError("no compiled workflow — call arbiterCompileWorkflow first")
	}
	if len(args) < 2 {
		return jsError("setSourceFacts requires source target and JSON facts array")
	}
	var facts []expert.Fact
	if err := json.Unmarshal([]byte(args[1].String()), &facts); err != nil {
		return jsError(err.Error())
	}
	if err := activeWorkflow.SetSourceFacts(args[0].String(), facts); err != nil {
		return jsError(err.Error())
	}
	return jsResult(map[string]any{"ok": true})
}

func runWorkflow(_ js.Value, args []js.Value) any {
	if activeWorkflow == nil {
		return jsError("no compiled workflow — call arbiterCompileWorkflow first")
	}
	result, err := activeWorkflow.Run(context.Background())
	if err != nil {
		return jsError(err.Error())
	}
	return jsWorkflowResult(result)
}

// --- JSON Helpers ---

func jsError(msg string) any {
	return map[string]any{"error": msg}
}

func jsResult(data map[string]any) any {
	blob, _ := json.Marshal(data)
	return js.Global().Get("JSON").Call("parse", string(blob))
}

func jsMatchedRules(matched []vm.MatchedRule) any {
	results := make([]map[string]any, len(matched))
	for i, m := range matched {
		results[i] = map[string]any{
			"name":   m.Name,
			"action": m.Action,
			"params": m.Params,
		}
	}
	blob, _ := json.Marshal(results)
	return js.Global().Get("JSON").Call("parse", string(blob))
}

func jsStrategyResult(result strategy.Result) any {
	data := map[string]any{
		"strategy": result.Strategy,
		"outcome":  result.Outcome,
		"selected": result.Selected,
		"params":   result.Params,
	}
	blob, _ := json.Marshal(data)
	return js.Global().Get("JSON").Call("parse", string(blob))
}

func jsExpertResult(result expert.Result) any {
	outcomes := make([]map[string]any, len(result.Outcomes))
	for i, o := range result.Outcomes {
		outcomes[i] = map[string]any{
			"rule":   o.Rule,
			"name":   o.Name,
			"params": o.Params,
		}
	}
	facts := make([]map[string]any, len(result.Facts))
	for i, f := range result.Facts {
		facts[i] = map[string]any{
			"type":   f.Type,
			"key":    f.Key,
			"fields": f.Fields,
		}
	}
	data := map[string]any{
		"outcomes":   outcomes,
		"facts":      facts,
		"stopReason": string(result.StopReason),
		"rounds":     result.Rounds,
		"mutations":  result.Mutations,
	}
	blob, _ := json.Marshal(data)
	return js.Global().Get("JSON").Call("parse", string(blob))
}

func jsWorkflowResult(result workflow.Result) any {
	arbiters := make(map[string]any, len(result.Arbiters))
	for name, run := range result.Arbiters {
		outcomes := make([]map[string]any, len(run.Delta.Outcomes))
		for i, o := range run.Delta.Outcomes {
			outcomes[i] = map[string]any{
				"rule":   o.Rule,
				"name":   o.Name,
				"params": o.Params,
			}
		}
		facts := make([]map[string]any, len(run.Delta.Facts))
		for i, f := range run.Delta.Facts {
			facts[i] = map[string]any{
				"type":   f.Type,
				"key":    f.Key,
				"fields": f.Fields,
			}
		}
		arbiters[name] = map[string]any{
			"outcomes":   outcomes,
			"facts":      facts,
			"stopReason": string(run.Delta.StopReason),
			"rounds":     run.Delta.Rounds,
			"mutations":  run.Delta.Mutations,
		}
	}
	data := map[string]any{
		"order":    result.Order,
		"arbiters": arbiters,
	}
	blob, _ := json.Marshal(data)
	return js.Global().Get("JSON").Call("parse", string(blob))
}
