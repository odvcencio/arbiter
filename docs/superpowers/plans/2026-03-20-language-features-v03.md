# Arbiter v0.3.0 Language Features Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add 7 language features to Arbiter — aggregates, multi-quantifier fix, let bindings, stable-round gating, string concatenation, flag else-if chains, and temporal fact conditions.

**Architecture:** Each feature touches a vertical slice through grammar → compiler → VM → tests. They are independent and can be implemented in any order. The grammar is defined via gotreesitter's Go DSL in `grammar.go`, compiled to bytecode by `compiler/compiler.go`, and executed by the stack VM in `vm/vm.go`. Expert session changes go in `expert/session.go`.

**Tech Stack:** Go 1.25, gotreesitter (tree-sitter pure-Go), custom bytecode VM with 4-byte fixed-width instructions.

## Execution Update

- [x] Task 1: Aggregates implemented end-to-end in grammar, compiler, VM, highlighting, and tests.
- [x] Task 2: Multi-quantifier `and` confirmed already working; added explicit regression coverage instead of changing parser behavior.
- [x] Task 3: Let bindings implemented in grammar/compiler/VM, with locals preserved into action param evaluation.
- [x] Task 4: `stable` expert-rule gating implemented, including deferred scheduling across a quiescent round.
- [x] Task 5: `+` now concatenates strings and stringifies mixed operands.
- [x] Task 6: Flag `else when` chains implemented as syntax sugar with parser + flag tests.
- [x] Task 7: Temporal fact metadata implemented via `asserted_round` / `__round` and `current_round`.

## Implementation Notes

- Aggregates were added to `_value_expr`, not `_cond_expr`, so they work in conditions and action params.
- Let bindings are compiled once in the condition and then made available to action params by preserving VM locals across match building.
- Stable rules needed scheduler support beyond a simple guard; deferred stable work now forces an extra quiet-round evaluation pass before the session can quiesce.
- Task 2’s original diagnosis was stale. The parser already handled multiple quantifiers joined by `and`; the shipped change is a regression test that locks that behavior in.

## Validation

- `go test . ./compiler ./expert ./flags ./vm -count=1`
- `go test ./... -count=1` still fails in the existing networked example suites under `examples/battletest` and `examples/ci-gateway` because they expect a live gRPC service.

---

## File Structure

| File | Responsibility | Tasks |
|------|---------------|-------|
| `compiler/opcode.go` | Opcode definitions | 1, 3, 5 |
| `compiler/compiler.go` | CST → bytecode compilation | 1, 2, 3, 5 |
| `vm/vm.go` | Bytecode interpreter | 1, 3, 5 |
| `grammar.go` | Tree-sitter grammar definition | 1, 2, 3, 6 |
| `expert/session.go` | Forward-chaining inference loop | 4, 7 |
| `flags/flags.go` | Flag rule parsing and evaluation | 6 |
| `highlights.scm` | Syntax highlighting queries | 1, 3, 6 |

Test files are colocated: `*_test.go` in each package, plus `examples/battletest/` for integration tests.

---

## Task 1: Aggregates (sum, count, avg)

Adds `sum(expr for var in collection)`, `count(var in collection)`, and `avg(expr for var in collection)` as expressions usable anywhere a value is expected — conditions, action params, expert rules.

**Syntax:**
```arb
when { sum(item.price for item in cart.items) > 1000 }
when { count(f in facts.RiskFlag) > 3 }
when { avg(score.value for score in facts.Score) > 0.8 }
```

**Files:**
- Modify: `grammar.go` — add `aggregate_expr` production
- Modify: `compiler/opcode.go` — add `OpAggBegin`, `OpAggAccum`, `OpAggEnd` opcodes (47, 48, 49) and `FlagSum`, `FlagCount`, `FlagAvg` constants
- Modify: `compiler/compiler.go` — add `compileAggregate` function, add case to `compileExpr` switch
- Modify: `vm/vm.go` — add aggregate handler to eval loop, extend `iterState` with accumulator
- Modify: `highlights.scm` — add `"sum"`, `"count"`, `"avg"` as `@keyword`
- Test: `eval_test.go` or new `eval_aggregate_test.go`

- [ ] **Step 1: Add grammar rule**

In `grammar.go`, after `quantifier_expr`, add:

```go
g.Define("aggregate_expr", Seq(
    Field("function", Choice(Str("sum"), Str("count"), Str("avg"))),
    Str("("),
    Choice(
        // sum(expr for var in collection) / avg(expr for var in collection)
        Seq(
            Field("value_expr", Sym("_expr")),
            Str("for"),
            Field("var", Sym("identifier")),
            Str("in"),
            Field("collection", Sym("_expr")),
        ),
        // count(var in collection) — no value expr, just counts items
        Seq(
            Field("var", Sym("identifier")),
            Str("in"),
            Field("collection", Sym("_expr")),
        ),
    ),
    Str(")"),
))
```

Add `Sym("aggregate_expr")` to the `_cond_expr` choice (not `_value_expr`, since aggregates return numbers used in comparisons).

- [ ] **Step 2: Add opcodes**

In `compiler/opcode.go`, add after `OpRuleMatch`:

```go
OpAggBegin OpCode = 47 // pop collection, begin aggregation (flags=FlagSum/FlagCount/FlagAvg, arg=varIdx)
OpAggAccum OpCode = 48 // pop value, accumulate (flags=kind)
OpAggEnd   OpCode = 49 // push aggregate result

const (
    FlagSum   uint8 = 0
    FlagCount uint8 = 1
    FlagAvg   uint8 = 2
)
```

- [ ] **Step 3: Write failing test**

In a new file `eval_aggregate_test.go`:

```go
func TestAggregateSum(t *testing.T) {
    rs, err := arbiter.Compile([]byte(`
        rule T {
            when { sum(item.price for item in cart.items) > 100 }
            then Match {}
        }
    `))
    if err != nil { t.Fatal(err) }
    dc := arbiter.DataFromMap(map[string]any{
        "cart": map[string]any{
            "items": []any{
                map[string]any{"price": float64(50)},
                map[string]any{"price": float64(60)},
            },
        },
    }, rs)
    matched, _ := arbiter.Eval(rs, dc)
    if len(matched) == 0 { t.Fatal("expected match: sum=110 > 100") }
}

func TestAggregateCount(t *testing.T) {
    rs, _ := arbiter.Compile([]byte(`
        rule T {
            when { count(item in cart.items) > 1 }
            then Match {}
        }
    `))
    dc := arbiter.DataFromMap(map[string]any{
        "cart": map[string]any{
            "items": []any{
                map[string]any{"name": "a"},
                map[string]any{"name": "b"},
            },
        },
    }, rs)
    matched, _ := arbiter.Eval(rs, dc)
    if len(matched) == 0 { t.Fatal("expected match: count=2 > 1") }
}

func TestAggregateAvg(t *testing.T) {
    rs, _ := arbiter.Compile([]byte(`
        rule T {
            when { avg(s.value for s in scores) > 7 }
            then Match {}
        }
    `))
    dc := arbiter.DataFromMap(map[string]any{
        "scores": []any{
            map[string]any{"value": float64(8)},
            map[string]any{"value": float64(9)},
            map[string]any{"value": float64(6)},
        },
    }, rs)
    matched, _ := arbiter.Eval(rs, dc)
    if len(matched) == 0 { t.Fatal("expected match: avg=7.67 > 7") }
}
```

Run: `go test ./ -run TestAggregate -v`
Expected: FAIL (compile error — grammar doesn't know aggregate_expr yet)

- [ ] **Step 4: Implement compiler**

In `compiler/compiler.go`, add case to `compileExpr` switch:

```go
case "aggregate_expr":
    return c.compileAggregate(code, n)
```

Add `compileAggregate` function:

```go
func (c *cstCompiler) compileAggregate(code []byte, n *gotreesitter.Node) []byte {
    funcName := c.text(c.childByField(n, "function"))
    varName := c.text(c.childByField(n, "var"))
    varIdx := c.pool.String(varName)

    var flag uint8
    switch funcName {
    case "sum":
        flag = FlagSum
    case "count":
        flag = FlagCount
    case "avg":
        flag = FlagAvg
    }

    // Compile collection
    code = c.compileExpr(code, c.childByField(n, "collection"))

    // AggBegin: pop collection, init accumulator
    code = Emit(code, OpAggBegin, flag, varIdx)

    // Compile value expression body (for sum/avg) or emit a constant 1 (for count)
    bodyStart := len(code)
    if valueExpr := c.childByField(n, "value_expr"); valueExpr != nil {
        code = c.compileExpr(code, valueExpr)
    } else {
        // count: push 1.0 for each item
        code = Emit(code, OpLoadNum, 0, c.pool.Number(1))
    }
    bodyLen := uint16(len(code) - bodyStart)

    // AggAccum: pop value, accumulate
    code = Emit(code, OpAggAccum, flag, bodyLen)

    // AggEnd: push result
    code = Emit(code, OpAggEnd, flag, 0)

    return code
}
```

- [ ] **Step 5: Implement VM handlers**

In `vm/vm.go`, add to the eval switch:

```go
case compiler.OpAggBegin:
    list := vm.pop()
    iter := iterState{
        kind:    flags, // reuse kind for agg type
        varName: vm.strPool.Get(arg),
        items:   vm.listEntries(list),
        result:  false, // unused for agg
    }
    // Store previous local if shadowed
    if vm.locals == nil {
        vm.locals = make(map[string]any)
    }
    if prev, ok := vm.locals[iter.varName]; ok {
        iter.prev = prev
        iter.hadPrev = true
    }
    vm.iters = append(vm.iters, iter)

    if len(iter.items) == 0 {
        // Empty collection: skip to AggEnd, push 0
        nextIP, found := vm.findMatchingAggAccum(instrs, ip, end)
        if found {
            ip = nextIP + compiler.InstrSize // skip past AggAccum to AggEnd
        }
        break
    }
    vm.locals[iter.varName] = iter.items[0]

case compiler.OpAggAccum:
    if len(vm.iters) == 0 {
        break
    }
    val := vm.toNum(vm.pop())
    iter := &vm.iters[len(vm.iters)-1]
    iter.aggSum += val
    iter.aggCount++
    iter.index++
    if iter.index < len(iter.items) {
        vm.locals[iter.varName] = iter.items[iter.index]
        ip -= uint32(arg) // jump back to body start (arg = body length)
        continue
    }

case compiler.OpAggEnd:
    if len(vm.iters) == 0 {
        vm.push(NumVal(0))
        break
    }
    iter := vm.iters[len(vm.iters)-1]
    vm.iters = vm.iters[:len(vm.iters)-1]
    if iter.hadPrev {
        vm.locals[iter.varName] = iter.prev
    } else {
        delete(vm.locals, iter.varName)
    }
    switch flags {
    case compiler.FlagSum:
        vm.push(NumVal(iter.aggSum))
    case compiler.FlagCount:
        vm.push(NumVal(float64(iter.aggCount)))
    case compiler.FlagAvg:
        if iter.aggCount == 0 {
            vm.push(NumVal(0))
        } else {
            vm.push(NumVal(iter.aggSum / float64(iter.aggCount)))
        }
    }
```

Extend `iterState` struct with aggregate fields:

```go
type iterState struct {
    // ... existing fields ...
    aggSum   float64
    aggCount int
}
```

Add `findMatchingAggAccum` (same pattern as `findMatchingIterNext` but matching `OpAggAccum`).

- [ ] **Step 6: Add highlight queries**

In `highlights.scm`, add to the Quantifiers section:

```scm
"sum" @keyword
"count" @keyword
"avg" @keyword
"for" @keyword
```

- [ ] **Step 7: Run tests, verify all pass**

Run: `go test ./... -count=1 -short`
Expected: All pass including new aggregate tests.

- [ ] **Step 8: Commit**

```
feat(lang): add sum/count/avg aggregate expressions
```

---

## Task 2: Multiple Quantifiers with `and`

Fix `any x in A { } and any y in B { }` which fails due to parser ambiguity between quantifier body braces and when-block closing brace.

**Root cause:** The `_expr` rule allows `and_expr` which contains `_expr and _expr`. When the first `_expr` is a `quantifier_expr` ending with `}`, the parser can't tell if `}` closes the quantifier or the when-block.

**Fix strategy:** Change quantifier body delimiter from `{ }` to `( )` would be breaking. Instead, add a `multi_quantifier_expr` production that handles the specific pattern. Or simpler: use tree-sitter precedence to prefer quantifier `}` over when-block `}`.

Actually, the real fix is in how the grammar defines the body. The quantifier's `}` is inside the `_expr` chain, and when the parser sees `} and`, it can close the quantifier and continue with `and`. The issue may actually be a gotreesitter parser limitation with nested same-delimiters. Let's test first.

**Files:**
- Modify: `grammar.go` — adjust quantifier or when_block to resolve ambiguity
- Test: `grammar_test.go` or `eval_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestMultiQuantifierAnd(t *testing.T) {
    source := `
    expert rule T {
        when {
            any risk in facts.RiskFlag { risk.level == "high" }
            and any txn in facts.Transaction { txn.amount > 1000 }
        }
        then assert Combined { key: "both" }
    }`
    _, err := arbiter.Compile([]byte(source))
    if err != nil {
        t.Fatalf("compile failed: %v", err)
    }
}
```

Run: `go test ./ -run TestMultiQuantifierAnd -v`
Expected: FAIL (parse error)

- [ ] **Step 2: Diagnose the exact parser conflict**

Use the gotreesitter parse tree to identify where the parser gets confused. The `quantifier_expr` body field consumes `_expr` which can include `and_expr`. The parser may greedily consume `} and any ...` as part of the outer expression.

Try adding the `quantifier_expr` body as a separate non-recursive rule, or use tree-sitter's `Prec` to make the quantifier closing `}` bind tightly.

- [ ] **Step 3: Fix grammar**

The fix is to ensure the quantifier body does NOT consume `and_expr` or `or_expr` at the top level. Change the quantifier body from `_expr` to `_cond_expr`:

In `grammar.go`, change `quantifier_expr`:

```go
g.Define("quantifier_expr", Seq(
    Field("quantifier", Choice(Str("any"), Str("all"), Str("none"))),
    Field("var", Sym("identifier")),
    Str("in"),
    Field("collection", Sym("_expr")),
    Str("{"),
    Field("body", Sym("_cond_expr")),  // was _expr — restricts body to non-logical
    Str("}"),
))
```

Wait — this would break `any x in items { x.a == 1 and x.b == 2 }` inside the body. The body NEEDS to support `and`/`or`.

**Alternative fix:** The real issue is the parser seeing the outer `and` as part of the quantifier body. Add a conflict declaration or use `Token` wrapping. Or change the when-block to not use `_expr` directly but a `when_body_expr` that handles the top-level `and` between quantifiers differently.

This requires deeper gotreesitter investigation. Mark as needs-investigation and move on. If the simple `_cond_expr` fix breaks inner-body logic, try adding explicit `Prec` or conflict resolution.

- [ ] **Step 4: Verify existing tests still pass**

Run: `go test ./... -count=1 -short`

- [ ] **Step 5: Commit**

```
fix(grammar): resolve multi-quantifier and-expression ambiguity
```

---

## Task 3: Let Bindings

Adds `let name = expr` declarations inside `when { }` blocks. The bound name is available as a local variable in subsequent expressions.

**Syntax:**
```arb
rule T {
    when {
        let total = income.wages + income.interest
        total > 50000
        and total < 100000
    }
    then Match { computed_total: total }
}
```

**Files:**
- Modify: `grammar.go` — add `let_binding` production, update `when_block` and `expert_when_block`
- Modify: `compiler/opcode.go` — add `OpSetLocal` opcode (50)
- Modify: `compiler/compiler.go` — add `compileLet` and update when-block compilation
- Modify: `vm/vm.go` — add `OpSetLocal` handler
- Modify: `highlights.scm` — add `"let"` as `@keyword`
- Test: `eval_test.go`

- [ ] **Step 1: Add grammar rule**

```go
g.Define("let_binding", Seq(
    Str("let"),
    Field("name", Sym("identifier")),
    Str("="),
    Field("value", Sym("_expr")),
))
```

Update `when_block` to allow let bindings before the expression:

```go
g.Define("when_block", Seq(
    Str("when"),
    Optional(Seq(Str("segment"), Field("segment", Sym("identifier")))),
    Str("{"),
    Repeat(Sym("let_binding")),
    Field("expr", Sym("_expr")),
    Str("}"),
))
```

Same for `expert_when_block`.

- [ ] **Step 2: Add opcode**

In `compiler/opcode.go`:

```go
OpSetLocal OpCode = 50 // pop value, store in locals[Constants.strings[arg]]
```

- [ ] **Step 3: Write failing test**

```go
func TestLetBinding(t *testing.T) {
    rs, err := arbiter.Compile([]byte(`
        rule T {
            when {
                let total = income.wages + income.interest
                total > 50000
            }
            then Match { amount: total }
        }
    `))
    if err != nil { t.Fatal(err) }
    dc := arbiter.DataFromMap(map[string]any{
        "income": map[string]any{"wages": float64(40000), "interest": float64(15000)},
    }, rs)
    matched, _ := arbiter.Eval(rs, dc)
    if len(matched) == 0 { t.Fatal("expected match: total=55000 > 50000") }
    if v, ok := matched[0].Params["amount"].(float64); !ok || v != 55000 {
        t.Fatalf("amount = %v, want 55000", matched[0].Params["amount"])
    }
}
```

- [ ] **Step 4: Implement compiler**

In `compileRule`, before compiling the condition expression, compile any let bindings:

```go
// Inside when-block compilation:
for i := 0; i < int(whenNode.NamedChildCount()); i++ {
    child := whenNode.NamedChild(i)
    if c.nodeType(child) == "let_binding" {
        nameNode := c.childByField(child, "name")
        valueNode := c.childByField(child, "value")
        code = c.compileExpr(code, valueNode)
        code = Emit(code, OpSetLocal, 0, c.pool.String(c.text(nameNode)))
    }
}
// Then compile the main expression
```

- [ ] **Step 5: Implement VM handler**

```go
case compiler.OpSetLocal:
    val := vm.pop()
    if vm.locals == nil {
        vm.locals = make(map[string]any)
    }
    vm.locals[vm.strPool.Get(arg)] = vm.valueToAny(val)
```

The local is then resolved by `OpLoadVar` which already checks `vm.lookupLocal()` before the DataContext.

- [ ] **Step 6: Add highlight query**

```scm
(let_binding "let" @keyword)
(let_binding name: (identifier) @variable.parameter)
```

- [ ] **Step 7: Run tests, verify all pass**

- [ ] **Step 8: Commit**

```
feat(lang): add let bindings in when blocks
```

---

## Task 4: Negative Fact Check Without Phase Gate (`stable` keyword)

Adds a `stable` keyword on expert rules that prevents the rule from firing until no new facts were asserted in the previous round (i.e., the system has reached a local fixed point for lower-priority rules).

**Syntax:**
```arb
expert rule AllClear priority 50 {
    stable
    when { none ps in facts.PlantStress { true } }
    then emit StatusReport { status: "optimal" }
}
```

**Files:**
- Modify: `grammar.go` — add `stable` named node, add to `expert_rule_declaration`
- Modify: `expert/compile.go` — extract `stable` field into `Rule.Stable`
- Modify: `expert/session.go` — skip stable rules unless previous round had zero mutations
- Modify: `highlights.scm` — add `(stable) @keyword`
- Test: `expert/expert_test.go`

- [ ] **Step 1: Add grammar**

```go
g.Define("stable", Str("stable"))
```

Add `Optional(Field("stable", Sym("stable")))` after `no_loop` in `expert_rule_declaration`.

- [ ] **Step 2: Add field to Rule struct**

In `expert/compile.go`, add `Stable bool` to `Rule` struct. Extract from AST: `rule.Stable = n.ChildByFieldName("stable", lang) != nil`.

- [ ] **Step 3: Write failing test**

```go
func TestSessionStableDefersUntilQuiescent(t *testing.T) {
    source := `
    expert rule SeedFact priority 5 {
        when { input.value > 0 }
        then assert Marker { key: "seed", done: true }
    }
    expert rule CheckClear priority 50 {
        stable
        when { none m in facts.Marker { m.done == true } }
        then emit AllClear { status: "clear" }
    }`
    // CheckClear should NOT fire because Marker exists after SeedFact runs.
    // Without stable, it would fire in round 1 before Marker is committed.
    prog, _ := expert.Compile([]byte(source))
    sess := expert.NewSession(prog, map[string]any{"input": map[string]any{"value": float64(1)}}, nil, expert.Options{})
    result, _ := sess.Run(context.Background())
    for _, o := range result.Outcomes {
        if o.Name == "AllClear" {
            t.Fatal("AllClear should not fire — Marker fact exists")
        }
    }
}
```

- [ ] **Step 4: Implement in session**

In `evalRound`, after the `shouldEvaluate` check, add:

```go
if rule.Stable && s.lastRoundMutations > 0 {
    continue // defer until system stabilizes
}
```

Add `lastRoundMutations int` to `Session` struct. Set it at the end of each round to the mutation count for that round.

- [ ] **Step 5: Add highlight**

```scm
(stable) @keyword
```

- [ ] **Step 6: Run tests, commit**

```
feat(expert): add stable keyword for deferred-until-quiescent rules
```

---

## Task 5: String Concatenation in Action Params

Make `+` work for strings in addition to numbers. When both operands are strings, concatenate. When mixed, coerce to string.

**Files:**
- Modify: `vm/vm.go` — update `OpAdd` handler to check types
- Test: `eval_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestStringConcat(t *testing.T) {
    rs, _ := arbiter.Compile([]byte(`
        rule T {
            when { true }
            then Alert { message: "Hello " + user.name }
        }
    `))
    dc := arbiter.DataFromMap(map[string]any{
        "user": map[string]any{"name": "World"},
    }, rs)
    matched, _ := arbiter.Eval(rs, dc)
    if matched[0].Params["message"] != "Hello World" {
        t.Fatalf("message = %v, want 'Hello World'", matched[0].Params["message"])
    }
}
```

- [ ] **Step 2: Update OpAdd handler**

In `vm/vm.go`, replace the `OpAdd` case:

```go
case compiler.OpAdd:
    b, a := vm.pop(), vm.pop()
    if a.Typ == TypeString || b.Typ == TypeString {
        // String concatenation
        vm.push(StrVal(vm.strPool.Intern(vm.toStr(a) + vm.toStr(b))))
    } else {
        vm.push(NumVal(vm.toNum(a) + vm.toNum(b)))
    }
```

`toStr` already exists for converting values to strings. `strPool.Intern` interns the result.

- [ ] **Step 3: Run tests, commit**

```
feat(vm): support string concatenation with + operator
```

---

## Task 6: Flag Else-If Chains

Add `else` keyword between flag rules for readability. This is pure syntax sugar — flag rules already evaluate top-to-bottom with first-match-wins. The `else` makes the intent explicit.

**Syntax:**
```arb
flag checkout type multivariate default "control" {
    when internal_users then "treatment_b"
    else when beta_cohort then "treatment_a"
    else when { true } rollout 10 then "treatment_a"
}
```

**Files:**
- Modify: `grammar.go` — add optional `"else"` before `flag_rule` within `_flag_body`
- Modify: `highlights.scm` — add `"else" @keyword` in flag context
- Test: `flags/flags_test.go`

- [ ] **Step 1: Update grammar**

The simplest approach: make `else` an optional prefix on `flag_rule`:

```go
g.Define("flag_rule", Seq(
    Optional(Str("else")),
    Str("when"),
    // ... rest unchanged
))
```

This makes `else` purely decorative — the evaluation doesn't change because flags are already first-match-wins.

- [ ] **Step 2: Write test**

```go
func TestFlagElseIfChain(t *testing.T) {
    source := []byte(`
    flag test type boolean default "false" {
        when { user.role == "admin" } then "true"
        else when { user.role == "mod" } then "true"
    }`)
    f, err := flags.Load(source)
    if err != nil { t.Fatal(err) }
    v := f.Variant("test", map[string]any{"user": map[string]any{"role": "mod"}})
    if v.Name != "true" { t.Fatalf("got %q", v.Name) }
}
```

- [ ] **Step 3: Add highlight**

```scm
(flag_rule "else" @keyword)
```

- [ ] **Step 4: Run tests, commit**

```
feat(flags): add else keyword for flag rule chains
```

---

## Task 7: Temporal Conditions

Add round metadata to expert facts so rules can reference when a fact was asserted.

**Design:** Add `AssertedRound int` to the `Fact` struct. Expose it as `__round` in the fact's fields when building the evaluation context. Rules can then write:

```arb
expert rule StaleCheck {
    when { any f in facts.Marker { f.__round < current_round - 3 } }
    then emit Stale { target: "Marker" }
}
```

**Files:**
- Modify: `expert/session.go` — add `AssertedRound` to `Fact`, set on assert, expose in eval context
- Test: `expert/expert_test.go`

- [ ] **Step 1: Add field to Fact**

```go
type Fact struct {
    Type          string
    Key           string
    Fields        map[string]any
    DerivedBy     []string
    AssertedRound int
}
```

- [ ] **Step 2: Set round on assert**

In `setDerivedSupport`, when creating/updating a fact, set `fact.AssertedRound = s.rounds`.

- [ ] **Step 3: Expose in eval context**

In `refreshContextView`, when building the facts map for evaluation, inject `__round` into each fact's fields:

```go
factFields["__round"] = float64(fact.AssertedRound)
```

Also inject `current_round` into the top-level envelope:

```go
s.evalCtx["current_round"] = float64(s.rounds)
```

- [ ] **Step 4: Write test**

```go
func TestSessionTemporalRoundTracking(t *testing.T) {
    source := `
    expert rule Seed priority 5 {
        when { input.go == true }
        then assert Marker { key: "test", value: 1 }
    }
    expert rule CheckAge priority 50 {
        stable
        when { any m in facts.Marker { m.__round < current_round } }
        then emit Aged { rounds_old: current_round }
    }`
    prog, _ := expert.Compile([]byte(source))
    sess := expert.NewSession(prog, map[string]any{"input": map[string]any{"go": true}}, nil, expert.Options{})
    result, _ := sess.Run(context.Background())
    // Marker asserted in round 1 (visible round 2), __round=1
    // CheckAge sees it in a stable round where current_round > __round
    found := false
    for _, o := range result.Outcomes {
        if o.Name == "Aged" { found = true }
    }
    if !found { t.Fatal("expected Aged outcome") }
}
```

- [ ] **Step 5: Run tests, commit**

```
feat(expert): add temporal round tracking to facts
```

---

## Execution Notes

- Tasks are independent. Any task can be implemented without the others.
- Task 2 (multi-quantifier) may need deeper gotreesitter investigation — start with the simple `_cond_expr` body restriction and test whether inner `and`/`or` still works.
- Task 5 (string concat) is the smallest change — good warmup.
- Task 1 (aggregates) is the highest GTM impact — prioritize for the blog post.
- Run `go test ./... -count=1 -short` after each task to verify no regressions.
- Run `go run ./cmd/arbiter check <file>` to validate grammar changes against example files.
