// compiler/jsonloader.go
package compiler

import (
	"encoding/json"
	"fmt"

	"github.com/odvcencio/arbiter/intern"
)

// JSONRuleInput holds the raw JSON strings for one rule.
type JSONRuleInput struct {
	Name      string
	Priority  int
	Condition string
	Action    string
}

// CompileJSONRule compiles a single Arishem-format JSON rule into bytecode.
func CompileJSONRule(name string, priority int, condJSON, actJSON string) (*CompiledRuleset, error) {
	return CompileJSONBatch([]JSONRuleInput{{name, priority, condJSON, actJSON}})
}

// CompileJSONBatch compiles multiple Arishem-format JSON rules into a single
// CompiledRuleset, sharing a constant pool across all rules.
func CompileJSONBatch(rules []JSONRuleInput) (*CompiledRuleset, error) {
	c := &jsonCompiler{
		pool: intern.NewPool(),
	}

	for _, r := range rules {
		if err := c.compileRule(r); err != nil {
			return nil, fmt.Errorf("rule %s: %w", r.Name, err)
		}
	}

	return &CompiledRuleset{
		Constants:    c.pool,
		Instructions: c.code,
		Rules:        c.rules,
		Actions:      c.actions,
	}, nil
}

type jsonCompiler struct {
	pool    *intern.Pool
	code    []byte
	rules   []RuleHeader
	actions []ActionEntry
}

func (c *jsonCompiler) compileRule(r JSONRuleInput) error {
	nameIdx := c.pool.String(r.Name)
	condOff := uint32(len(c.code))

	// Parse and compile condition.
	var cond map[string]any
	if err := json.Unmarshal([]byte(r.Condition), &cond); err != nil {
		return fmt.Errorf("parse condition: %w", err)
	}
	if err := c.compileNode(cond); err != nil {
		return fmt.Errorf("compile condition: %w", err)
	}
	c.code = Emit(c.code, OpRuleMatch, 0, 0)
	condLen := uint32(len(c.code)) - condOff

	// Parse and compile action.
	actionIdx := uint16(len(c.actions))
	var act map[string]any
	if r.Action != "" {
		if err := json.Unmarshal([]byte(r.Action), &act); err != nil {
			return fmt.Errorf("parse action: %w", err)
		}
	}
	if err := c.compileActionJSON(act); err != nil {
		return fmt.Errorf("compile action: %w", err)
	}

	c.rules = append(c.rules, RuleHeader{
		NameIdx:      nameIdx,
		Priority:     int32(r.Priority),
		ConditionOff: condOff,
		ConditionLen: condLen,
		ActionIdx:    actionIdx,
	})

	return nil
}

func (c *jsonCompiler) compileNode(node map[string]any) error {
	// Logical: {"OpLogic": "&&", "Conditions": [...]}
	if opLogic, ok := node["OpLogic"].(string); ok {
		conditions, err := jsonNodes(node["Conditions"], "Conditions")
		if err != nil {
			return err
		}
		if opLogic == "not" && len(conditions) > 0 {
			if err := c.compileNode(conditions[0]); err != nil {
				return err
			}
			c.code = Emit(c.code, OpNot, 0, 0)
			return nil
		}
		jumpOp := OpJumpIfFalse
		logicOp := OpAnd
		if opLogic == "||" {
			jumpOp = OpJumpIfTrue
			logicOp = OpOr
		}
		for i, cond := range conditions {
			if err := c.compileNode(cond); err != nil {
				return err
			}
			if i < len(conditions)-1 {
				// Emit short-circuit jump, compile next condition, emit logic op, backpatch.
				jumpPos := len(c.code)
				c.code = Emit(c.code, jumpOp, 0, 0)

				if err := c.compileNode(conditions[i+1]); err != nil {
					return err
				}
				c.code = Emit(c.code, logicOp, 0, 0)
				dist := uint16(len(c.code) - jumpPos - InstrSize)
				c.code[jumpPos+2] = byte(dist)
				c.code[jumpPos+3] = byte(dist >> 8)

				// Chain remaining conditions.
				for j := i + 2; j < len(conditions); j++ {
					jumpPos2 := len(c.code)
					c.code = Emit(c.code, jumpOp, 0, 0)
					if err := c.compileNode(conditions[j]); err != nil {
						return err
					}
					c.code = Emit(c.code, logicOp, 0, 0)
					dist2 := uint16(len(c.code) - jumpPos2 - InstrSize)
					c.code[jumpPos2+2] = byte(dist2)
					c.code[jumpPos2+3] = byte(dist2 >> 8)
				}
				return nil
			}
		}
		return nil
	}

	if _, ok := node["ForeachOperator"]; ok {
		return c.compileForeach(node)
	}

	// Comparison/Collection/String: {"Operator": "==", "Lhs": {...}, "Rhs": {...}}
	if operator, ok := node["Operator"].(string); ok {
		if lhs, has := node["Lhs"]; has {
			lhsNode, err := jsonNode(lhs, "Lhs")
			if err != nil {
				return err
			}
			if err := c.compileValue(lhsNode); err != nil {
				return err
			}
		}
		if rhs, has := node["Rhs"]; has {
			rhsNode, err := jsonNode(rhs, "Rhs")
			if err != nil {
				return err
			}
			if err := c.compileValue(rhsNode); err != nil {
				return err
			}
		}
		c.emitOperator(operator)
		return nil
	}

	// MathExpr at top level.
	if mathExpr, has := node["MathExpr"]; has {
		mathNode, err := jsonNode(mathExpr, "MathExpr")
		if err != nil {
			return err
		}
		return c.compileNode(mathNode)
	}

	// Value node — delegate.
	return c.compileValue(node)
}

func (c *jsonCompiler) compileValue(node map[string]any) error {
	// Variable reference.
	if varExpr, ok := node["VarExpr"].(string); ok {
		idx := c.pool.String(varExpr)
		c.code = Emit(c.code, OpLoadVar, 0, idx)
		return nil
	}

	// Constant value.
	if constVal, ok := node["Const"].(map[string]any); ok {
		if s, ok := constVal["StrConst"].(string); ok {
			idx := c.pool.String(s)
			c.code = Emit(c.code, OpLoadStr, 0, idx)
		} else if n, ok := constVal["NumConst"].(float64); ok {
			idx := c.pool.Number(n)
			c.code = Emit(c.code, OpLoadNum, 0, idx)
		} else if b, ok := constVal["BoolConst"].(bool); ok {
			arg := uint16(0)
			if b {
				arg = 1
			}
			c.code = Emit(c.code, OpLoadBool, 0, arg)
		} else {
			c.code = Emit(c.code, OpLoadNull, 0, 0)
		}
		return nil
	}

	// Constant list.
	if constList, ok := node["ConstList"].([]any); ok {
		var items []intern.PoolValue
		for i, item := range constList {
			m, err := jsonNode(item, fmt.Sprintf("ConstList[%d]", i))
			if err != nil {
				return err
			}
			if s, ok := m["StrConst"].(string); ok {
				items = append(items, intern.PoolValue{Typ: intern.TypeString, Str: c.pool.String(s)})
			} else if n, ok := m["NumConst"].(float64); ok {
				items = append(items, intern.PoolValue{Typ: intern.TypeNumber, Num: n})
			} else if b, ok := m["BoolConst"].(bool); ok {
				items = append(items, intern.PoolValue{Typ: intern.TypeBool, Bool: b})
			}
		}
		idx, length := c.pool.List(items)
		// Encode as two instructions: first carries list index, second carries length.
		// VM recognises OpLoadNull with flags=TypeList as list-load.
		c.code = Emit(c.code, OpLoadNull, intern.TypeList, idx)
		c.code = Emit(c.code, OpLoadNull, 0xFF, length)
		return nil
	}

	// Math expression used as a value.
	if mathExpr, has := node["MathExpr"]; has {
		mathNode, err := jsonNode(mathExpr, "MathExpr")
		if err != nil {
			return err
		}
		return c.compileNode(mathNode)
	}

	// Fallback: null.
	c.code = Emit(c.code, OpLoadNull, 0, 0)
	return nil
}

func (c *jsonCompiler) compileForeach(node map[string]any) error {
	logic, _ := node["ForeachLogic"].(string)
	var flag uint8
	switch logic {
	case "||":
		flag = FlagAny
	case "!||":
		flag = FlagNone
	default:
		flag = FlagAll
	}

	varName, ok := node["ForeachVar"].(string)
	if !ok || varName == "" {
		return fmt.Errorf("ForeachVar: expected non-empty string")
	}

	lhs, err := jsonNode(node["Lhs"], "Lhs")
	if err != nil {
		return err
	}
	if err := c.compileValue(lhs); err != nil {
		return err
	}

	varIdx := c.pool.String(varName)
	c.code = Emit(c.code, OpIterBegin, flag, varIdx)

	body, err := jsonNode(node["Condition"], "Condition")
	if err != nil {
		return err
	}
	bodyStart := len(c.code)
	if err := c.compileNode(body); err != nil {
		return err
	}
	bodyLen := uint16(len(c.code) - bodyStart)

	c.code = Emit(c.code, OpIterNext, flag, bodyLen)
	c.code = Emit(c.code, OpIterEnd, flag, 0)
	return nil
}

func (c *jsonCompiler) emitOperator(op string) {
	switch op {
	case "==":
		c.code = Emit(c.code, OpEq, 0, 0)
	case "!=":
		c.code = Emit(c.code, OpNeq, 0, 0)
	case ">":
		c.code = Emit(c.code, OpGt, 0, 0)
	case ">=":
		c.code = Emit(c.code, OpGte, 0, 0)
	case "<":
		c.code = Emit(c.code, OpLt, 0, 0)
	case "<=":
		c.code = Emit(c.code, OpLte, 0, 0)
	case "LIST_IN":
		c.code = Emit(c.code, OpIn, 0, 0)
	case "!LIST_IN":
		c.code = Emit(c.code, OpNotIn, 0, 0)
	case "LIST_CONTAINS":
		c.code = Emit(c.code, OpContains, 0, 0)
	case "!LIST_CONTAINS":
		c.code = Emit(c.code, OpNotContains, 0, 0)
	case "LIST_RETAIN":
		c.code = Emit(c.code, OpRetains, 0, 0)
	case "!LIST_RETAIN":
		c.code = Emit(c.code, OpNotRetains, 0, 0)
	case "LIST_VAGUE_CONTAINS":
		c.code = Emit(c.code, OpVagueContains, 0, 0)
	case "SUBSET_OF":
		c.code = Emit(c.code, OpSubsetOf, 0, 0)
	case "SUPERSET_OF":
		c.code = Emit(c.code, OpSupersetOf, 0, 0)
	case "STRING_START_WITH":
		c.code = Emit(c.code, OpStartsWith, 0, 0)
	case "STRING_END_WITH":
		c.code = Emit(c.code, OpEndsWith, 0, 0)
	case "CONTAIN_REGEXP":
		c.code = Emit(c.code, OpMatches, 0, 0)
	case "IS_NULL":
		c.code = Emit(c.code, OpIsNull, 0, 0)
	case "!IS_NULL":
		c.code = Emit(c.code, OpIsNotNull, 0, 0)
	case "BETWEEN_ALL_CLOSE":
		c.code = Emit(c.code, OpBetweenCC, 0, 0)
	case "BETWEEN_ALL_OPEN":
		c.code = Emit(c.code, OpBetweenOO, 0, 0)
	case "BETWEEN_LEFT_CLOSE_RIGHT_OPEN":
		c.code = Emit(c.code, OpBetweenCO, 0, 0)
	case "BETWEEN_LEFT_OPEN_RIGHT_CLOSE":
		c.code = Emit(c.code, OpBetweenOC, 0, 0)
	case "+":
		c.code = Emit(c.code, OpAdd, 0, 0)
	case "-":
		c.code = Emit(c.code, OpSub, 0, 0)
	case "*":
		c.code = Emit(c.code, OpMul, 0, 0)
	case "/":
		c.code = Emit(c.code, OpDiv, 0, 0)
	case "%":
		c.code = Emit(c.code, OpMod, 0, 0)
	}
}

func (c *jsonCompiler) compileActionJSON(act map[string]any) error {
	if act == nil {
		c.actions = append(c.actions, ActionEntry{})
		return nil
	}
	name, _ := act["ActionName"].(string)
	nameIdx := c.pool.String(name)
	var params []ActionParam

	if paramMap, ok := act["ParamMap"].(map[string]any); ok {
		for key, val := range paramMap {
			keyIdx := c.pool.String(key)
			valOff := uint32(len(c.code))
			vm, err := jsonNode(val, "ParamMap."+key)
			if err != nil {
				return err
			}
			if err := c.compileValue(vm); err != nil {
				return err
			}
			valLen := uint32(len(c.code)) - valOff
			params = append(params, ActionParam{KeyIdx: keyIdx, ValueOff: valOff, ValueLen: valLen})
		}
	}

	c.actions = append(c.actions, ActionEntry{NameIdx: nameIdx, Params: params})
	return nil
}

func jsonNode(v any, field string) (map[string]any, error) {
	node, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected object", field)
	}
	return node, nil
}

func jsonNodes(v any, field string) ([]map[string]any, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected array", field)
	}
	nodes := make([]map[string]any, 0, len(items))
	for i, item := range items {
		node, err := jsonNode(item, fmt.Sprintf("%s[%d]", field, i))
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}
