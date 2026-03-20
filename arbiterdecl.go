package arbiter

import (
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/arbiter/internal/parseutil"
	gotreesitter "github.com/odvcencio/gotreesitter"
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

func compileArbiters(root *gotreesitter.Node, source []byte, lang *gotreesitter.Language) ([]ArbiterDeclaration, error) {
	out := make([]ArbiterDeclaration, 0)
	seen := make(map[string]struct{})
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type(lang) != "arbiter_declaration" {
			continue
		}
		decl, err := compileArbiterDeclaration(child, source, lang)
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

func compileArbiterDeclaration(n *gotreesitter.Node, source []byte, lang *gotreesitter.Language) (ArbiterDeclaration, error) {
	nameNode := n.ChildByFieldName("name", lang)
	if nameNode == nil {
		return ArbiterDeclaration{}, fmt.Errorf("arbiter declaration missing name")
	}

	decl := ArbiterDeclaration{
		Name:     nodeText(nameNode, source),
		Killable: true,
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		switch child.Type(lang) {
		case "arbiter_poll_clause":
			intervalNode := child.ChildByFieldName("interval", lang)
			if intervalNode == nil {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: poll interval is required", decl.Name)
			}
			interval, err := time.ParseDuration(nodeText(intervalNode, source))
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
		case "arbiter_stream_clause":
			targetNode := child.ChildByFieldName("target", lang)
			if targetNode == nil {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: stream target is required", decl.Name)
			}
			decl.Triggers = append(decl.Triggers, ArbiterTrigger{
				Kind:   ArbiterTriggerStream,
				Target: arbiterLiteral(nodeText(targetNode, source)),
			})
		case "arbiter_schedule_clause":
			exprNode := child.ChildByFieldName("expr", lang)
			if exprNode == nil {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: schedule expression is required", decl.Name)
			}
			trigger := ArbiterTrigger{
				Kind:     ArbiterTriggerSchedule,
				Schedule: arbiterLiteral(nodeText(exprNode, source)),
			}
			if targetNode := child.ChildByFieldName("target", lang); targetNode != nil {
				trigger.Target = arbiterLiteral(nodeText(targetNode, source))
			}
			decl.Triggers = append(decl.Triggers, trigger)
		case "arbiter_source_clause":
			targetNode := child.ChildByFieldName("target", lang)
			if targetNode == nil {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: source target is required", decl.Name)
			}
			decl.Sources = append(decl.Sources, ArbiterSource{
				Target: arbiterLiteral(nodeText(targetNode, source)),
			})
		case "arbiter_checkpoint_clause":
			targetNode := child.ChildByFieldName("target", lang)
			if targetNode == nil {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: checkpoint target is required", decl.Name)
			}
			if decl.Checkpoint != "" {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: checkpoint may only be declared once", decl.Name)
			}
			decl.Checkpoint = arbiterLiteral(nodeText(targetNode, source))
		case "arbiter_handler_clause":
			outcomeNode := child.ChildByFieldName("outcome", lang)
			kindNode := child.ChildByFieldName("kind", lang)
			if outcomeNode == nil || kindNode == nil {
				return ArbiterDeclaration{}, fmt.Errorf("arbiter %s: handler must declare outcome and kind", decl.Name)
			}
			handler := ArbiterHandler{
				Outcome: nodeText(outcomeNode, source),
				Kind:    ArbiterHandlerKind(nodeText(kindNode, source)),
			}
			if filterNode := child.ChildByFieldName("filter", lang); filterNode != nil {
				if exprNode := filterNode.ChildByFieldName("expr", lang); exprNode != nil {
					handler.Where = strings.TrimSpace(nodeText(exprNode, source))
				}
			}
			if targetNode := child.ChildByFieldName("target", lang); targetNode != nil {
				handler.Target = arbiterLiteral(nodeText(targetNode, source))
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

func arbiterLiteral(raw string) string {
	return parseutil.StripQuotes(raw)
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
