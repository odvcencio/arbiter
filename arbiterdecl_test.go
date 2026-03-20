package arbiter

import "testing"

func TestCompileFullExtractsArbiters(t *testing.T) {
	result, err := CompileFull([]byte(`
arbiter trading_system {
	stream wss://exchange.com/prices
	schedule "0 8 * * MON-FRI" source https://calendar.api/market-hours
	poll 30s
	source chain://risk_facts
	checkpoint /var/lib/arbiter/trading.state
	on Opportunity where confidence > 0.8 chain ai_analysis
	on RiskAlert where severity == "critical" exec "kill-all-orders"
	on RiskAlert where severity == "warning" slack #trading-risk
	on * audit /var/log/trading.jsonl
}

rule NeedsWater {
	when { true }
	then WaterAction { duration_minutes: 15 }
}
`))
	if err != nil {
		t.Fatalf("CompileFull: %v", err)
	}
	if len(result.Arbiters) != 1 {
		t.Fatalf("expected 1 arbiter declaration, got %+v", result.Arbiters)
	}
	decl := result.Arbiters[0]
	if decl.Name != "trading_system" {
		t.Fatalf("unexpected arbiter name: %+v", decl)
	}
	if !decl.Killable {
		t.Fatalf("expected arbiters to be killable by default: %+v", decl)
	}
	if len(decl.Triggers) != 3 {
		t.Fatalf("expected 3 triggers, got %+v", decl.Triggers)
	}
	if decl.Triggers[0].Kind != ArbiterTriggerStream || decl.Triggers[0].Target != "wss://exchange.com/prices" {
		t.Fatalf("unexpected stream trigger: %+v", decl.Triggers[0])
	}
	if decl.Triggers[1].Kind != ArbiterTriggerSchedule || decl.Triggers[1].Schedule != "0 8 * * MON-FRI" || decl.Triggers[1].Target != "https://calendar.api/market-hours" {
		t.Fatalf("unexpected schedule trigger: %+v", decl.Triggers[1])
	}
	if decl.Triggers[2].Kind != ArbiterTriggerPoll || decl.Triggers[2].Interval.String() != "30s" {
		t.Fatalf("unexpected poll trigger: %+v", decl.Triggers[2])
	}
	if len(decl.Sources) != 1 {
		t.Fatalf("expected 1 source, got %+v", decl.Sources)
	}
	if decl.Sources[0].Target != "chain://risk_facts" {
		t.Fatalf("unexpected first source: %+v", decl.Sources[0])
	}
	if decl.Checkpoint != "/var/lib/arbiter/trading.state" {
		t.Fatalf("unexpected checkpoint: %+v", decl)
	}
	if len(decl.Handlers) != 4 {
		t.Fatalf("expected 4 handlers, got %+v", decl.Handlers)
	}
	if decl.Handlers[0].Kind != ArbiterHandlerChain || decl.Handlers[0].Outcome != "Opportunity" || decl.Handlers[0].Where != "confidence > 0.8" {
		t.Fatalf("unexpected chain handler: %+v", decl.Handlers[0])
	}
	if decl.Handlers[2].Kind != ArbiterHandlerSlack || decl.Handlers[2].Target != "#trading-risk" || decl.Handlers[2].Where != `severity == "warning"` {
		t.Fatalf("unexpected slack handler: %+v", decl.Handlers[2])
	}
	if decl.Handlers[3].Kind != ArbiterHandlerAudit || decl.Handlers[3].Outcome != "*" {
		t.Fatalf("unexpected audit handler: %+v", decl.Handlers[3])
	}
	if len(result.Ruleset.Rules) != 1 {
		t.Fatalf("expected rule compilation to remain intact, got %d rules", len(result.Ruleset.Rules))
	}
}

func TestCompileFullRejectsArbiterWithoutTrigger(t *testing.T) {
	_, err := CompileFull([]byte(`
arbiter greenhouse {
	source https://sensors.local/soil
	on * stdout
}
`))
	if err == nil || err.Error() != "arbiter greenhouse: at least one trigger is required" {
		t.Fatalf("expected missing trigger error, got %v", err)
	}
}

func TestCompileFullRejectsStdoutTarget(t *testing.T) {
	_, err := CompileFull([]byte(`
arbiter greenhouse {
	poll 30s
	on * stdout /tmp/out
}
`))
	if err == nil || err.Error() != "arbiter greenhouse: handler stdout does not take a target" {
		t.Fatalf("expected stdout target error, got %v", err)
	}
}

func TestCompileFullRejectsZeroPollInterval(t *testing.T) {
	_, err := CompileFull([]byte(`
arbiter greenhouse {
	poll 0s
	on * stdout
}
`))
	if err == nil || err.Error() != "arbiter greenhouse: poll interval must be greater than zero" {
		t.Fatalf("expected zero poll interval error, got %v", err)
	}
}

func TestCompileFullRejectsInvalidChainTarget(t *testing.T) {
	_, err := CompileFull([]byte(`
arbiter greenhouse {
	poll 30s
	on Opportunity chain "https://example.com/not-an-arbiter"
}
`))
	if err == nil || err.Error() != "arbiter greenhouse: chain target must be an arbiter identifier" {
		t.Fatalf("expected invalid chain target error, got %v", err)
	}
}

func TestCompileFullRejectsDuplicateArbiterNames(t *testing.T) {
	_, err := CompileFull([]byte(`
arbiter trading {
	poll 30s
	on * stdout
}

arbiter trading {
	stream wss://exchange.com/prices
	on * audit /tmp/trading.jsonl
}
`))
	if err == nil || err.Error() != `duplicate arbiter "trading"` {
		t.Fatalf("expected duplicate arbiter error, got %v", err)
	}
}
