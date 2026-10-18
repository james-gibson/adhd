package scenario

import "testing"

// TestSmokeAlarmRequired pins the predicate that decides whether a missing
// ocd-smoke-alarm is a skip or a failure. CI and the release workflow depend on
// it refusing to skip, so the values that mean "not required" are pinned here —
// a loosened default would silently turn the release gate back into a no-op.
func TestSmokeAlarmRequired(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"anything-else", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{"  ", false},
		{" false ", false},
	}

	for _, tc := range tests {
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv(requireSmokeAlarmEnv, tc.value)
			if got := smokeAlarmRequired(); got != tc.want {
				t.Errorf("%s=%q: smokeAlarmRequired() = %v, want %v",
					requireSmokeAlarmEnv, tc.value, got, tc.want)
			}
		})
	}
}
