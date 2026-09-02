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

func TestOwnMentionIdentityAndPriority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		text      string
		ownNode   uint32
		shortName string
		want      string
		ok        bool
	}{
		{name: "node ID", text: "hello @!698508e0", ownNode: 0x698508e0, shortName: "GubB", want: "@!698508e0", ok: true},
		{name: "node ID without short name", text: "@!698508e0 hello", ownNode: 0x698508e0, want: "@!698508e0", ok: true},
		{name: "node ID case insensitive", text: "@!698508E0 hello", ownNode: 0x698508e0, shortName: "GubB", want: "@!698508E0", ok: true},
		{name: "short name fallback", text: "hello @gubb", ownNode: 0x698508e0, shortName: "GubB", want: "@gubb", ok: true},
		{name: "node ID has priority", text: "@GubB first @!698508e0 second", ownNode: 0x698508e0, shortName: "GubB", want: "@!698508e0", ok: true},
		{name: "different node ID", text: "@!698508e1 hello", ownNode: 0x698508e0, shortName: "GubB", ok: false},
		{name: "node ID boundary", text: "@!698508e0x hello", ownNode: 0x698508e0, shortName: "GubB", ok: false},
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
