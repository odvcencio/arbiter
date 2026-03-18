package govern

// Trace records evaluation decisions. It is not goroutine-safe.
type Trace struct {
	Steps []TraceStep `json:"steps,omitempty"`
}

// TraceStep records one governance check.
type TraceStep struct {
	Check  string `json:"check"`
	Result bool   `json:"result"`
	Detail string `json:"detail"`
}

// Append adds a trace step. It is a no-op on a nil receiver.
func (t *Trace) Append(check string, result bool, detail string) {
	if t == nil {
		return
	}
	t.Steps = append(t.Steps, TraceStep{
		Check:  check,
		Result: result,
		Detail: detail,
	})
}
