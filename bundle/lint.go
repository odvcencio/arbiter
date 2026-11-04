package bundle

import (
	"fmt"
	"strings"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/compiler"
)

// Signal is one structured evidence fact extracted from a compiled ruleset.
// Signals are the input to the edge export policy — they describe what the
// analyzer found, not what to do about it.
type Signal struct {
	Kind   string   `json:"kind"`
	Count  int      `json:"count,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	Values []string `json:"values,omitempty"`
}

// AnalyzeResult is the output of the static analyzer + policy evaluation.
type AnalyzeResult struct {
	Signals  []Signal `json:"signals"`
	Decision string   `json:"decision"` // "allow", "warn", "block"
	Reasons  []string `json:"reasons,omitempty"`
}

// DefaultEdgeExportPolicy is the built-in conservative policy.
const DefaultEdgeExportPolicy = `
# Default edge export policy.
# Evaluates BundleSignal facts and decides: allow, warn, or block.

rule BlockMoneyLiterals priority 1 {
    when {
        signal.kind == "money_literal"
        and signal.count > 0
    }
    then Block { reason: "bundle contains monetary values" }
}

rule BlockCryptoLiterals priority 1 {
    when {
        signal.kind == "crypto_literal"
        and signal.count > 0
    }
    then Block { reason: "bundle contains cryptocurrency values" }
}

rule BlockRiskPaths priority 2 {
    when {
        signal.kind == "risk_path"
        and signal.count > 0
    }
    then Block { reason: "bundle contains risk/fraud variable paths" }
}

rule WarnThresholds priority 10 {
    when {
        signal.kind == "threshold_literal"
        and signal.count > 3
    }
    then Warn { reason: "bundle contains many numeric thresholds" }
}

rule WarnPrereqs priority 11 {
    when {
        signal.kind == "prereq_chain"
        and signal.count > 2
    }
    then Warn { reason: "bundle has deep prerequisite chains" }
}

rule AllowByDefault priority 99 {
    when { true }
    then Allow { reason: "no sensitive patterns detected" }
}
`

// Analyze extracts signals from a compiled ruleset and evaluates them against
// the edge export policy. Pass nil for policySource to use the default policy.
func Analyze(rs *compiler.CompiledRuleset, policySource []byte) (AnalyzeResult, error) {
	if rs == nil {
		return AnalyzeResult{Decision: "block", Reasons: []string{"nil ruleset"}}, nil
	}

	signals := ExtractSignals(rs)

	if policySource == nil {
		policySource = []byte(DefaultEdgeExportPolicy)
	}

	result := AnalyzeResult{Signals: signals}

	// Evaluate each signal against the policy.
	policyProg, err := arbiter.Compile(policySource)
	if err != nil {
		return result, fmt.Errorf("compile edge export policy: %w", err)
	}

	decision := "allow"
	for _, sig := range signals {
		ctx := map[string]any{
			"signal": map[string]any{
				"kind":  sig.Kind,
				"count": float64(sig.Count),
			},
		}
		dc := arbiter.DataFromMap(ctx, policyProg)
		matched, _, err := arbiter.EvalGoverned(policyProg, dc, policyProg.Segments, ctx)
		if err != nil {
			return result, fmt.Errorf("evaluate edge export policy for %s: %w", sig.Kind, err)
		}
		for _, m := range matched {
			reason, _ := m.Params["reason"].(string)
			switch m.Action {
			case "Block":
				decision = "block"
				if reason != "" {
					result.Reasons = append(result.Reasons, reason)
				}
			case "Warn":
				if decision != "block" {
					decision = "warn"
				}
				if reason != "" {
					result.Reasons = append(result.Reasons, reason)
				}
			}
		}
	}
	result.Decision = decision
	return result, nil
}

// ExtractSignals performs static analysis on a compiled ruleset and returns
// structured evidence facts. These describe what the analyzer found —
// they don't decide what to do about it.
func ExtractSignals(rs *compiler.CompiledRuleset) []Signal {
	if rs == nil {
		return nil
	}
	var signals []Signal
	strs := rs.Constants.Strings()

	// Numeric thresholds.
	nums := rs.Constants.Numbers()
	var thresholds []string
	for _, n := range nums {
		if n == 0 || n == 1 || n == -1 {
			continue
		}
		thresholds = append(thresholds, fmt.Sprintf("%g", n))
	}
	if len(thresholds) > 0 {
		signals = append(signals, Signal{Kind: "threshold_literal", Count: len(thresholds), Values: thresholds})
	}

	// Decimal/currency values — separate money from crypto.
	decs := rs.Constants.Decimals()
	cryptoUnits := map[string]bool{"BTC": true, "ETH": true, "SOL": true, "USDC": true, "USDT": true}
	moneyUnits := map[string]bool{"USD": true, "EUR": true, "GBP": true, "JPY": true, "CNY": true, "CHF": true, "CAD": true, "AUD": true}
	var moneyValues, cryptoValues []string
	for _, d := range decs {
		unit := d.Unit()
		if cryptoUnits[unit] {
			cryptoValues = append(cryptoValues, d.String())
		} else if moneyUnits[unit] || unit == "" {
			moneyValues = append(moneyValues, d.String())
		}
	}
	if len(moneyValues) > 0 {
		signals = append(signals, Signal{Kind: "money_literal", Count: len(moneyValues), Values: moneyValues})
	}
	if len(cryptoValues) > 0 {
		signals = append(signals, Signal{Kind: "crypto_literal", Count: len(cryptoValues), Values: cryptoValues})
	}

	// Sensitive variable paths.
	sensitivePatterns := []string{
		"risk", "score", "fraud", "price", "amount", "threshold",
		"salary", "income", "credit", "balance", "limit", "budget",
	}
	var riskPaths []string
	for _, s := range strs {
		lower := strings.ToLower(s)
		for _, pattern := range sensitivePatterns {
			if strings.Contains(lower, pattern) {
				riskPaths = append(riskPaths, s)
				break
			}
		}
	}
	if len(riskPaths) > 0 {
		signals = append(signals, Signal{Kind: "risk_path", Count: len(riskPaths), Paths: riskPaths})
	}

	// Prerequisite chain depth.
	if len(rs.Prereqs) > 0 {
		signals = append(signals, Signal{Kind: "prereq_chain", Count: len(rs.Prereqs)})
	}

	// Rollout usage.
	rolloutCount := 0
	for _, r := range rs.Rules {
		if r.HasRollout && r.RolloutBps > 0 {
			rolloutCount++
		}
	}
	if rolloutCount > 0 {
		signals = append(signals, Signal{Kind: "rollout_usage", Count: rolloutCount})
	}

	// Rule/strategy/worker counts.
	signals = append(signals, Signal{Kind: "rule_count", Count: len(rs.Rules)})

	return signals
}

// --- Legacy Warning type for backward compatibility ---

// Warning is one lint warning about edge-shipping a ruleset.
type Warning struct {
	Severity string // "warn" or "info"
	Message  string
}

// LintForEdge is the legacy heuristic linter. Use Analyze() for policy-based decisions.
func LintForEdge(rs *compiler.CompiledRuleset) []Warning {
	result, err := Analyze(rs, nil)
	if err != nil {
		return []Warning{{Severity: "warn", Message: err.Error()}}
	}
	var warnings []Warning
	for _, reason := range result.Reasons {
		severity := "info"
		if result.Decision == "block" {
			severity = "warn"
		} else if result.Decision == "warn" {
			severity = "warn"
		}
		warnings = append(warnings, Warning{Severity: severity, Message: reason})
	}
	return warnings
}
