package arbtest

import (
	"testing"
	"time"
)

func TestParseStepOffsetHourAlias(t *testing.T) {
	for _, in := range []string{"1h", "1hr"} {
		got, err := parseStepOffset(in)
		if err != nil || got != time.Hour {
			t.Errorf("parseStepOffset(%q) = %v, %v; want 1h", in, got, err)
		}
	}
}
