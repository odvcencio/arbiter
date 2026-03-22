package arbiter

import (
	"fmt"
	"time"

	"github.com/odvcencio/arbiter/ir"
)

// ArbiterDeclaration is one continuously running decision loop declared in .arb.
type ArbiterDeclaration struct {
	Name       string
	Killable   bool
	Triggers   []ArbiterTrigger
	Sources    []ArbiterSource
	Checkpoint string
	Handlers   []ArbiterHandler
}

// ArbiterTriggerKind is the cadence mode for an arbiter.
type ArbiterTriggerKind string

const (
	ArbiterTriggerPoll     ArbiterTriggerKind = "poll"
	ArbiterTriggerStream   ArbiterTriggerKind = "stream"
	ArbiterTriggerSchedule ArbiterTriggerKind = "schedule"
)

// ArbiterTrigger is one activation mode for a continuous arbiter.
type ArbiterTrigger struct {
	Kind     ArbiterTriggerKind
	Interval time.Duration
	Schedule string
	Target   string
}

// ArbiterSource is one fact source referenced by an arbiter.
type ArbiterSource struct {
	Target string
}

// ArbiterHandlerKind is the pluggable sink for emitted outcomes.
type ArbiterHandlerKind string

const (
	ArbiterHandlerWebhook ArbiterHandlerKind = "webhook"
	ArbiterHandlerSlack   ArbiterHandlerKind = "slack"
	ArbiterHandlerChain   ArbiterHandlerKind = "chain"
	ArbiterHandlerExec    ArbiterHandlerKind = "exec"
	ArbiterHandlerGRPC    ArbiterHandlerKind = "grpc"
	ArbiterHandlerAudit   ArbiterHandlerKind = "audit"
	ArbiterHandlerStdout  ArbiterHandlerKind = "stdout"
)

// ArbiterHandler routes matching outcomes to one sink.
type ArbiterHandler struct {
	Outcome string
	Where   string
	Kind    ArbiterHandlerKind
	Target  string
}

func compileArbiters(program *ir.Program) ([]ArbiterDeclaration, error) {
	if program == nil {
		return nil, nil
	}
	out := make([]ArbiterDeclaration, 0, len(program.Arbiters))
	seen := make(map[string]struct{}, len(program.Arbiters))
	for i := range program.Arbiters {
		decl, err := compileArbiterDeclaration(program, &program.Arbiters[i])
		if err != nil {
			return nil, err
		}
		if _, ok := seen[decl.Name]; ok {
			return nil, fmt.Errorf("duplicate arbiter %q", decl.Name)
		}
		seen[decl.Name] = struct{}{}
		out = append(out, decl)
	}
	return out, nil
}

func compileArbiterDeclaration(program *ir.Program, declIR *ir.Arbiter) (ArbiterDeclaration, error) {
	if declIR == nil {
		return ArbiterDeclaration{}, fmt.Errorf("nil arbiter declaration")
	}
	if declIR.Name == "" {
		return ArbiterDeclaration{}, fmt.Errorf("arbiter declaration missing name")
	}

	decl := ArbiterDeclaration{
		Name:     declIR.Name,
		Killable: true,
	}

	for _, clause := range declIR.Clauses {
		switch clause.Kind {
		case ir.ArbiterPollClause:
			if clause.Interval == "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: poll interval is required", decl.Name)
			}
			interval, err := time.ParseDuration(clause.Interval)
			if err != nil {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: parse poll interval: %w", decl.Name, err)
			}
			if interval <= 0 {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: poll interval must be greater than zero", decl.Name)
			}
			decl.Triggers = append(decl.Triggers, ArbiterTrigger{
				Kind:     ArbiterTriggerPoll,
				Interval: interval,
			})
		case ir.ArbiterStreamClause:
			if clause.Target == "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: stream target is required", decl.Name)
			}
			decl.Triggers = append(decl.Triggers, ArbiterTrigger{
				Kind:   ArbiterTriggerStream,
				Target: clause.Target,
			})
		case ir.ArbiterScheduleClause:
			if clause.Expr == "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: schedule expression is required", decl.Name)
			}
			decl.Triggers = append(decl.Triggers, ArbiterTrigger{
				Kind:     ArbiterTriggerSchedule,
				Schedule: clause.Expr,
				Target:   clause.Target,
			})
		case ir.ArbiterSourceClause:
			if clause.Target == "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: source target is required", decl.Name)
			}
			decl.Sources = append(decl.Sources, ArbiterSource{Target: clause.Target})
		case ir.ArbiterCheckpointClause:
			if clause.Target == "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: checkpoint target is required", decl.Name)
			}
			if decl.Checkpoint != "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: checkpoint may only be declared once", decl.Name)
			}
			decl.Checkpoint = clause.Target
		case ir.ArbiterHandlerClause:
			if clause.Outcome == "" || clause.Handler == "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: handler must declare outcome and kind", decl.Name)
			}
			handler := ArbiterHandler{
				Outcome: clause.Outcome,
				Kind:    ArbiterHandlerKind(clause.Handler),
				Target:  clause.Target,
			}
			if clause.HasFilter {
				handler.Where = ir.RenderExpr(program, clause.Filter)
			}
			if handler.Kind != ArbiterHandlerStdout && handler.Target == "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: handler %s requires a target", decl.Name, handler.Kind)
			}
			if handler.Kind == ArbiterHandlerStdout && handler.Target != "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: handler %s does not take a target", decl.Name, handler.Kind)
			}
			if handler.Kind == ArbiterHandlerChain && !isArbiterIdentifier(handler.Target) {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: chain target must be an arbiter identifier", decl.Name)
			}
			decl.Handlers = append(decl.Handlers, handler)
		}
	}

	if len(decl.Triggers) == 0 {
		return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: at least one trigger is required", decl.Name)
	}
	return decl, nil
}

func isArbiterIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
			continue
		case r >= 'a' && r <= 'z':
			continue
		case r >= 'A' && r <= 'Z':
			continue
		case i > 0 && r >= '0' && r <= '9':
			continue
		default:
			return false
		}
	}
	return true
}
