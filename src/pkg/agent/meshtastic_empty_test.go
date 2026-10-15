// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/channels/meshtastic"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestMeshtasticInboundEmptyResponsePolicy(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		al, _, cleanup := newHookTestLoop(t, &multiToolProvider{})
		bc := &config.Channel{Type: config.ChannelMeshtastic}
		bc.SetName("mesh_home")
		ch, err := meshtastic.NewChannel(bc, &config.MeshtasticSettings{
			Transport: "http", HTTPAddress: "radio.invalid", SuppressErrors: suppress,
		}, al.bus.(*bus.MessageBus))
		if err != nil {
			t.Fatal(err)
		}
		manager, err := channels.NewManager(&config.Config{}, al.bus.(*bus.MessageBus), nil)
		if err != nil {
			t.Fatal(err)
		}
		// No StartAll: this exercises real channel policy without radio or workers.
		manager.RegisterChannel("mesh_home", ch)
		al.channelManager = manager
		response, err := al.processMessage(context.Background(), bus.InboundMessage{
			Context: bus.InboundContext{Channel: "mesh_home", ChatID: "channel:0", SenderID: "meshtastic:!aabbccdd"},
			Content: "hello",
		})
		want := defaultResponse
		if suppress {
			want = ""
		}
		if err != nil || response != want {
			t.Fatalf("suppress=%v response=%q want=%q err=%v", suppress, response, want, err)
		}
		cleanup()
	}
}
