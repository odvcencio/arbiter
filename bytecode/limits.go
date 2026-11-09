package bytecode

// MaxValueStackDepth bounds the VM's fixed-size value stack. The compiler's
// static stack-depth check rejects any expression that would need more, so a
// deep expression fails at compile time instead of at evaluation time.
const MaxValueStackDepth = 256

// MaxSafeInteger is 2^53 - 1, the largest magnitude the integer numeric profile
// accepts for a literal or an arithmetic result.
const MaxSafeInteger = 1<<53 - 1

// KillSwitchState records whether a declaration omitted kill_switch, enabled it,
// or explicitly set it off.
type KillSwitchState string

const (
	KillSwitchUnset KillSwitchState = ""
	KillSwitchOn    KillSwitchState = "on"
	KillSwitchOff   KillSwitchState = "off"
)

// IsSet reports whether the declaration mentioned kill_switch explicitly.
func (s KillSwitchState) IsSet() bool {
	return s != KillSwitchUnset
}

// Enabled reports whether the declaration enables the kill switch.
func (s KillSwitchState) Enabled() bool {
	return s == KillSwitchOn
}
