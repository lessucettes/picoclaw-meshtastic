// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import "testing"

func TestMentionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{"@BOT1 hello", true},
		{"x@BOT1 hello", false},
		{"@BOT12 hello", false},
	} {
		start, end, ok := mentionAt(tc.text, "BOT1")
		if ok != tc.ok {
			t.Errorf("mentionAt(%q) ok=%v, want %v", tc.text, ok, tc.ok)
		}
		if ok && tc.text[start] != '@' || ok && end <= start {
			t.Errorf("invalid match bounds %d..%d", start, end)
		}
	}
}

func TestOwnMentionIdentityAndPriority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		text      string
		ownNode   uint32
		shortName string
		want      string
		ok        bool
	}{
		{name: "node ID", text: "hello @!0badc0de", ownNode: 0x0badc0de, shortName: "BOT1", want: "@!0badc0de", ok: true},
		{name: "node ID without short name", text: "@!0badc0de hello", ownNode: 0x0badc0de, want: "@!0badc0de", ok: true},
		{name: "node ID case insensitive", text: "@!0BADC0DE hello", ownNode: 0x0badc0de, shortName: "BOT1", want: "@!0BADC0DE", ok: true},
		{name: "short name fallback", text: "hello @bot1", ownNode: 0x0badc0de, shortName: "BOT1", want: "@bot1", ok: true},
		{name: "node ID has priority", text: "@BOT1 first @!0badc0de second", ownNode: 0x0badc0de, shortName: "BOT1", want: "@!0badc0de", ok: true},
		{name: "different node ID", text: "@!0badc0df hello", ownNode: 0x0badc0de, shortName: "BOT1", ok: false},
		{name: "node ID boundary", text: "@!0badc0dex hello", ownNode: 0x0badc0de, shortName: "BOT1", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end, ok := ownMentionAt(tc.text, tc.ownNode, tc.shortName)
			if ok != tc.ok {
				t.Fatalf("ownMentionAt(%q) ok=%v, want %v", tc.text, ok, tc.ok)
			}
			if ok && tc.text[start:end] != tc.want {
				t.Errorf("ownMentionAt(%q) matched %q, want %q", tc.text, tc.text[start:end], tc.want)
			}
		})
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
