// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestPublishResponseForInboundIfNeeded_PreservesMessageID(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	inbound := &bus.InboundContext{
		Channel:   "meshtastic",
		ChatID:    "!000000b2",
		ChatType:  "direct",
		SenderID:  "meshtastic:!000000b2",
		MessageID: "151467856",
	}
	al.publishResponseForInboundIfNeeded(
		context.Background(),
		"meshtastic",
		"!000000b2",
		"session-1",
		"final reply",
		inbound,
	)

	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Context.MessageID != "151467856" {
			t.Fatalf("outbound message ID = %q, want 151467856", outbound.Context.MessageID)
		}
		if outbound.Context.ChatType != "direct" || outbound.Context.SenderID != "meshtastic:!000000b2" {
			t.Fatalf("outbound context was not preserved: %+v", outbound.Context)
		}
	case <-time.After(time.Second):
		t.Fatal("expected final outbound")
	}
}
