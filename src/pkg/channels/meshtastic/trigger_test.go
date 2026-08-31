// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import "testing"

func TestMentionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{"@Бот привет", true},
		{"x@Бот привет", false},
		{"@Ботик привет", false},
	} {
		start, end, ok := mentionAt(tc.text, "Бот")
		if ok != tc.ok {
			t.Errorf("mentionAt(%q) ok=%v, want %v", tc.text, ok, tc.ok)
		}
		if ok && tc.text[start] != '@' || ok && end <= start {
			t.Errorf("invalid match bounds %d..%d", start, end)
		}
	}
}

func TestExactDutyMinutes(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    int
		ok      bool
	}{
		{"Duty cycle limit exceeded. You can send again in 0 mins", 0, true},
		{"Duty cycle limit exceeded. You can send again in 60 mins", 60, true},
		{"Duty cycle limit exceeded. You can send again in 61 mins", 0, false},
		{"Duty cycle limit exceeded. You can send again in +1 mins", 0, false},
		{"Duty cycle limit exceeded. You can send again in 1 mins.", 0, false},
	} {
		got, ok := exactDutyMinutes(tc.message)
		if got != tc.want || ok != tc.ok {
			t.Errorf("exactDutyMinutes(%q)=(%d,%v), want (%d,%v)", tc.message, got, ok, tc.want, tc.ok)
		}
	}
}
