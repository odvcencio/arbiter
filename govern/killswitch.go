package govern

// IsKillSwitched reports whether evaluation should be skipped.
func IsKillSwitched(enabled bool, trace *Trace) bool {
	if !enabled {
		return false
	}
	trace.Append("kill_switch", true, "outcome is kill-switched")
	return true
}
