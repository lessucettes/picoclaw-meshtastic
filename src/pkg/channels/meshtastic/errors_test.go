// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestSuppressErrorsSettings(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		want          bool
	}{
		{name: "omitted"},
		{name: "disabled", setting: `,"suppress_errors":false`},
		{name: "enabled", setting: `,"suppress_errors":true`, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg config.Config
			raw := fmt.Sprintf(`{"channel_list":{"mesh_home":{"type":"meshtastic","settings":{"transport":"http","http_address":"radio"%s}}}}`, tc.setting)
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatal(err)
			}
			if err := config.InitChannelList(cfg.Channels); err != nil {
				t.Fatal(err)
			}
			decoded, err := cfg.Channels["mesh_home"].GetDecoded()
			if err != nil {
				t.Fatal(err)
			}
			c, err := NewChannel(cfg.Channels["mesh_home"], decoded.(*config.MeshtasticSettings), bus.NewMessageBus())
			if err != nil || c.cfg.SuppressErrors != tc.want || c.SuppressErrors() != tc.want {
				t.Fatalf("channel=%+v err=%v want suppress_errors=%v", c, err, tc.want)
			}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip config.Config
			if err := json.Unmarshal(data, &roundTrip); err != nil {
				t.Fatal(err)
			}
			decoded, err = roundTrip.Channels["mesh_home"].GetDecoded()
			if err != nil || decoded.(*config.MeshtasticSettings).SuppressErrors != tc.want {
				t.Fatalf("round trip settings=%+v err=%v", decoded, err)
			}
		})
	}
}

func TestSuppressErrorsEnvironmentOverride(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PICOCLAW_CHANNELS_MESHTASTIC_SUPPRESS_ERRORS", value)
			var cfg config.Config
			if err := json.Unmarshal([]byte(`{"channel_list":{"mesh_a":{"type":"meshtastic","settings":{"suppress_errors":false}},"mesh_b":{"type":"meshtastic","settings":{"suppress_errors":true}}}}`), &cfg); err != nil {
				t.Fatal(err)
			}
			if err := config.InitChannelList(cfg.Channels); err != nil {
				t.Fatal(err)
			}
			for name, bc := range cfg.Channels {
				decoded, err := bc.GetDecoded()
				if err != nil || decoded.(*config.MeshtasticSettings).SuppressErrors != (value == "true") {
					t.Fatalf("%s overrides=%+v err=%v", name, decoded, err)
				}
			}
		})
	}
}

func TestSendSuppressErrors(t *testing.T) {
	for _, chatID := range []string{"channel:0", "!aabbccdd"} {
		for _, tc := range []struct {
			name     string
			suppress bool
			raw      map[string]string
			wantSent bool
		}{
			{name: "default-sends-errors", raw: map[string]string{"is_error": "true"}, wantSent: true},
			{name: "suppress-errors", suppress: true, raw: map[string]string{"is_error": "true"}},
			{name: "ordinary-error-text", suppress: true, wantSent: true},
			{name: "explicit-success", suppress: true, raw: map[string]string{"is_error": "false"}, wantSent: true},
		} {
			t.Run(chatID+"/"+tc.name, func(t *testing.T) {
				c, ft := prepareSendChannel(t, false)
				c.cfg.SuppressErrors = tc.suppress
				ids, err := c.Send(context.Background(), bus.OutboundMessage{
					ChatID: chatID, Content: "Error processing message: quoted text",
					Context: bus.InboundContext{Raw: tc.raw},
				})
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if tc.wantSent {
					want = 1
				}
				if len(ids) != want || len(ft.submitted) != want || len(c.pending) != 0 {
					t.Fatalf("ids=%v submitted=%d pending=%d, want sent=%d", ids, len(ft.submitted), len(c.pending), want)
				}
				if !tc.wantSent && (!c.lastPacing.IsZero() || c.sentBot.contains(sentKey{id: 1234, channel: 0}, c.now())) {
					t.Fatal("suppressed error consumed pacing or populated the sent cache")
				}
			})
		}
	}
}

func TestSuppressErrorsDoesNotWaitForRadio(t *testing.T) {
	c, _ := newTestChannel(t, "mesh_home", config.GroupTriggerConfig{})
	c.cfg.SuppressErrors = true
	c.SetRunning(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ids, err := c.Send(ctx, bus.OutboundMessage{
		Content: "runtime diagnostic", Context: bus.InboundContext{Raw: map[string]string{"is_error": "true"}},
	})
	if err != nil || len(ids) != 0 {
		t.Fatalf("suppressed error waited for routing or radio: ids=%v err=%v", ids, err)
	}
}
