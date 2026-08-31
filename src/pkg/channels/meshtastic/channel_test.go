// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func intp(v int) *int { return &v }

func newTestChannel(t *testing.T, name string, group config.GroupTriggerConfig) (*Channel, *bus.MessageBus) {
	t.Helper()
	bc := &config.Channel{Type: config.ChannelMeshtastic, AllowFrom: []string{"*"}, GroupTrigger: group}
	bc.SetName(name)
	b := bus.NewMessageBus()
	c, err := NewChannel(bc, &config.MeshtasticSettings{Transport: "http", HTTPAddress: "radio.local"}, b)
	if err != nil {
		t.Fatal(err)
	}
	return c, b
}

func TestSettingsDefaultsAndValidation(t *testing.T) {
	valid, indices, address, err := validateSettings(&config.MeshtasticSettings{Transport: "http", HTTPAddress: "[::1]"})
	if err != nil || *valid.TextChunkBytes != 200 || *valid.SendDelayMS != 2000 || len(indices) != 1 || indices[0] != 0 || address != "[::1]:80" {
		t.Fatalf("defaults=%+v indices=%v address=%q err=%v", valid, indices, address, err)
	}
	for _, tc := range []config.MeshtasticSettings{
		{Transport: "http", HTTPAddress: "radio", TextChunkBytes: intp(0)},
		{Transport: "http", HTTPAddress: "radio", SendDelayMS: intp(1999)},
		{Transport: "http", HTTPAddress: "radio", ChannelIndices: []int{}},
		{Transport: "http", HTTPAddress: "radio", ChannelIndices: []int{1, 1}},
		{Transport: "http", HTTPAddress: "https://radio"},
		{Transport: "http", HTTPAddress: "http://radio"},
		{Transport: "http", HTTPAddress: "radio/path"},
		{Transport: "http", HTTPAddress: "radio", SerialPort: "/dev/ttyUSB0"},
		{Transport: "serial", SerialPort: "/dev/ttyUSB0", HTTPAddress: "radio"},
		{Transport: "serial"},
		{Transport: "mqtt"},
	} {
		if _, _, _, err := validateSettings(&tc); err == nil {
			t.Errorf("expected rejection for %+v", tc)
		}
	}
	serialCfg, _, _, err := validateSettings(&config.MeshtasticSettings{Transport: "serial", SerialPort: "/dev/ttyUSB0", ChannelIndices: []int{7}, TextChunkBytes: intp(1), SendDelayMS: intp(2000)})
	if err != nil || serialCfg.SerialPort != "/dev/ttyUSB0" {
		t.Fatalf("valid serial rejected: %v", err)
	}
}

func TestNormalFactorySupportsNamedInstances(t *testing.T) {
	raw := `{"channel_list":{"meshtastic":{"enabled":true,"type":"meshtastic","allow_from":["*"],"settings":{"transport":"http","http_address":"one.local"}},"mesh_a":{"enabled":true,"type":"meshtastic","allow_from":["*"],"settings":{"transport":"http","http_address":"a.local","channel_indices":[1]}},"mesh_b":{"enabled":true,"type":"meshtastic","allow_from":["*"],"settings":{"transport":"serial","serial_port":"/dev/null","channel_indices":[2]}}}}`
	var cfg config.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := config.InitChannelList(cfg.Channels); err != nil {
		t.Fatal(err)
	}
	m, err := channels.NewManager(&cfg, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"meshtastic", "mesh_a", "mesh_b"} {
		ch, ok := m.GetChannel(name)
		if !ok || ch.Name() != name {
			t.Fatalf("instance %q missing or misnamed: %v %v", name, ch, ok)
		}
	}
}

func TestTypeWideEnvironmentOverridesNamedInstances(t *testing.T) {
	t.Setenv("PICOCLAW_CHANNELS_MESHTASTIC_TEXT_CHUNK_BYTES", "144")
	t.Setenv("PICOCLAW_CHANNELS_MESHTASTIC_CHANNEL_INDICES", "2,3")
	var cfg config.Config
	raw := `{"channel_list":{"mesh_a":{"type":"meshtastic","settings":{"transport":"http","http_address":"a","text_chunk_bytes":100}},"mesh_b":{"type":"meshtastic","settings":{"transport":"http","http_address":"b","text_chunk_bytes":120}}}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := config.InitChannelList(cfg.Channels); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mesh_a", "mesh_b"} {
		decoded, err := cfg.Channels[name].GetDecoded()
		if err != nil {
			t.Fatal(err)
		}
		settings := decoded.(*config.MeshtasticSettings)
		if settings.TextChunkBytes == nil || *settings.TextChunkBytes != 144 || len(settings.ChannelIndices) != 2 || settings.ChannelIndices[0] != 2 || settings.ChannelIndices[1] != 3 {
			t.Fatalf("%s overrides=%+v", name, settings)
		}
	}
}

func TestRouteReplyAndPacketConstruction(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	c.indices = []uint32{3, 1}
	dm, err := c.parseRoute(bus.OutboundMessage{ChatID: "!aabbccdd"})
	if err != nil || !dm.direct || dm.destination != 0xaabbccdd || dm.channel != 3 || !dm.proactiveDM {
		t.Fatalf("proactive DM=%+v err=%v", dm, err)
	}
	dm, err = c.parseRoute(bus.OutboundMessage{ChatID: "!aabbccdd", Context: bus.InboundContext{Raw: map[string]string{"meshtastic_channel_index": "0"}}})
	if err != nil || dm.channel != 0 || dm.proactiveDM {
		t.Fatalf("reply DM=%+v err=%v", dm, err)
	}
	group, err := c.parseRoute(bus.OutboundMessage{ChatID: "channel:0"})
	if err != nil || group.direct || group.destination != broadcastNode || group.channel != 0 {
		t.Fatalf("group=%+v err=%v", group, err)
	}
	for _, bad := range []string{"!00000000", "!ffffffff", "!AABBCCDD", "channel:00", "channel:8", "other"} {
		if _, err := c.parseRoute(bus.OutboundMessage{ChatID: bad}); err == nil || !errors.Is(err, channels.ErrSendFailed) {
			t.Errorf("route %q error=%v", bad, err)
		}
	}
	if got, err := parseReplyID(bus.OutboundMessage{Context: bus.InboundContext{MessageID: "7"}, ReplyToMessageID: "9"}); err != nil || got != 7 {
		t.Fatalf("reply ID=%d err=%v", got, err)
	}
	p, err := buildTextEnvelope(0x11223344, group, 99, 7, "hello")
	if err != nil {
		t.Fatal(err)
	}
	mp := p.GetPacket()
	if mp.GetFrom() != 0x11223344 || mp.GetTo() != broadcastNode || mp.GetChannel() != 0 || mp.GetId() != 99 || !mp.GetWantAck() || mp.GetHopLimit() != 0 || mp.GetPriority() != mesh.MeshPacket_UNSET {
		t.Fatalf("packet fields: %+v", mp)
	}
	if d := mp.GetDecoded(); d.GetPortnum() != mesh.PortNum_TEXT_MESSAGE_APP || string(d.GetPayload()) != "hello" || d.GetReplyId() != 7 || d.GetWantResponse() {
		t.Fatalf("data fields: %+v", d)
	}
}

func readyAttempt(c *Channel) *attemptState {
	a := &attemptState{ctx: context.Background(), ownNode: 0x01020304, gotMyInfo: true, shortName: "Бот", channels: map[uint32]mesh.Channel_Role{0: mesh.Channel_PRIMARY, 1: mesh.Channel_SECONDARY}}
	c.setAttempt(a, false)
	c.publishReady(a)
	return a
}

func textPacket(from, to, id, channel, reply uint32, text string) *mesh.MeshPacket {
	d := &mesh.Data{Portnum: mesh.PortNum_TEXT_MESSAGE_APP, Payload: []byte(text), ReplyId: reply}
	p := &mesh.MeshPacket{From: from, To: to, Id: id, Channel: channel, HopStart: 3, HopLimit: 1, ViaMqtt: true, PkiEncrypted: true, RxSnr: 0}
	p.SetRxRssi(0)
	p.SetDecoded(d)
	return p
}

func TestInboundRoutingTriggersAndMetadata(t *testing.T) {
	c, b := newTestChannel(t, "mesh_a", config.GroupTriggerConfig{Prefixes: []string{"бот:"}})
	a := readyAttempt(c)
	c.runCtx, c.cancel = context.WithCancel(context.Background())
	c.wg.Add(1)
	go c.dispatcher()
	defer func() { c.cancel(); c.wg.Wait() }()

	p := textPacket(0xaabbccdd, a.ownNode, 10, 1, 9, "привет")
	c.handleInboundPacket(a, p)
	select {
	case got := <-b.InboundChan():
		if got.Context.Channel != "mesh_a" || got.Context.ChatID != "!aabbccdd" || got.Context.ChatType != "direct" || got.Context.SenderID != "meshtastic:!aabbccdd" || got.Context.MessageID != "10" || got.Context.ReplyToMessageID != "9" {
			t.Fatalf("context=%+v", got.Context)
		}
		if got.Sender.Platform != "meshtastic" || got.Sender.PlatformID != "!aabbccdd" || got.Sender.CanonicalID != "meshtastic:!aabbccdd" || got.Sender.Username != "" || got.Sender.DisplayName != "!aabbccdd" {
			t.Fatalf("sender=%+v", got.Sender)
		}
		raw := got.Context.Raw
		for key, want := range map[string]string{"meshtastic_rx_snr": "0", "meshtastic_rx_rssi": "0", "meshtastic_hops_away": "2", "meshtastic_via_mqtt": "true", "meshtastic_pki_encrypted": "true", "meshtastic_trigger": "dm"} {
			if raw[key] != want {
				t.Errorf("raw[%s]=%q, want %q", key, raw[key], want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("DM was not published")
	}

	// Duplicate packet is ignored, while a new packet with the same text is not.
	c.handleInboundPacket(a, p)
	c.handleInboundPacket(a, textPacket(0xaabbccdd, a.ownNode, 11, 1, 0, "привет"))
	select {
	case <-b.InboundChan():
	case <-time.After(time.Second):
		t.Fatal("new packet was incorrectly deduplicated")
	}

	// Public traffic needs a mention, configured prefix, or native reply.
	c.handleInboundPacket(a, textPacket(0xaabbccdd, broadcastNode, 12, 0, 0, "шум"))
	select {
	case got := <-b.InboundChan():
		t.Fatalf("unrelated group traffic published: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	c.handleInboundPacket(a, textPacket(0xaabbccdd, broadcastNode, 13, 0, 0, "@Бот привет"))
	select {
	case got := <-b.InboundChan():
		if got.Content != "привет" || got.Context.Raw["meshtastic_trigger"] != "mention" || !got.Context.Mentioned || got.Context.ChatID != "channel:0" {
			t.Fatalf("mention result=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("mention was not published")
	}
	c.handleInboundPacket(a, textPacket(0xaabbccdd, broadcastNode, 14, 0, 0, "бот: вопрос"))
	select {
	case got := <-b.InboundChan():
		if got.Content != "вопрос" || got.Context.Raw["meshtastic_trigger"] != "prefix" {
			t.Fatalf("prefix result=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("prefix was not published")
	}
}

func TestControlAcceptanceRequiresQueueAndEcho(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	a := readyAttempt(c)
	w := &acceptance{id: 55, destination: broadcastNode, attempt: a, wake: make(chan struct{}, 1)}
	c.pending[55] = w
	c.recordQueueStatus(&mesh.QueueStatus{MeshPacketId: 55, Res: 0, Free: 2})
	if w.terminal != terminalPending || !w.queueOK {
		t.Fatalf("queue alone completed waiter: %+v", w)
	}
	c.recordPacketControl(a, textPacket(a.ownNode, broadcastNode, 55, 0, 0, "echo"))
	if w.terminal != terminalAccepted {
		t.Fatalf("pair did not accept waiter: %+v", w)
	}

	w2 := &acceptance{id: 56, destination: broadcastNode, attempt: a, wake: make(chan struct{}, 1)}
	c.pending[56] = w2
	c.recordQueueStatus(&mesh.QueueStatus{MeshPacketId: 56, Res: 32, Free: 0})
	if w2.terminal != terminalQueueFull {
		t.Fatalf("queue full classified as %v", w2.terminal)
	}
}

func TestControlAcceptanceUsesQuietWindowWithoutFirmwareEcho(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	c.rejectionGrace = 10 * time.Millisecond
	a := readyAttempt(c)
	w := &acceptance{id: 59, destination: broadcastNode, attempt: a, wake: make(chan struct{}, 1)}
	c.pending[59] = w
	c.recordQueueStatus(&mesh.QueueStatus{MeshPacketId: 59, Res: 0, Free: 2})

	kind, _, at, _ := c.waitAcceptance(context.Background(), w)
	if kind != terminalAccepted || !at.Equal(w.queueAt) || w.echoOK {
		t.Fatalf("quiet-window acceptance = kind %v at %v waiter %+v", kind, at, w)
	}
}

func TestLocalRoutingErrorWinsQueueStatusQuietWindow(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	c.rejectionGrace = time.Second
	a := readyAttempt(c)
	w := &acceptance{id: 60, destination: broadcastNode, attempt: a, wake: make(chan struct{}, 1)}
	c.pending[60] = w
	c.recordQueueStatus(&mesh.QueueStatus{MeshPacketId: 60, Res: 0, Free: 2})

	routingMessage := &mesh.Routing{}
	routingMessage.SetErrorReason(mesh.Routing_RATE_LIMIT_EXCEEDED)
	routingPayload, err := proto.Marshal(routingMessage)
	if err != nil {
		t.Fatal(err)
	}
	routing := textPacket(a.ownNode, a.ownNode, 61, 0, 0, "")
	routing.SetDecoded(&mesh.Data{Portnum: mesh.PortNum_ROUTING_APP, Payload: routingPayload, RequestId: 60})
	c.recordPacketControl(a, routing)
	if w.terminal != terminalRate {
		t.Fatalf("local rejection after QueueStatus classified as %v", w.terminal)
	}
}

func TestControlAcceptanceRecognizesSimRadioTextEcho(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	a := readyAttempt(c)

	compressed, err := proto.Marshal(&mesh.Compressed{Portnum: mesh.PortNum_TEXT_MESSAGE_APP, Data: []byte("echo")})
	if err != nil {
		t.Fatal(err)
	}
	simulatorEcho := textPacket(a.ownNode, broadcastNode, 57, 0, 0, "")
	simulatorEcho.SetDecoded(&mesh.Data{Portnum: mesh.PortNum_SIMULATOR_APP, Payload: compressed})
	w := &acceptance{id: 57, destination: broadcastNode, attempt: a, queueOK: true, wake: make(chan struct{}, 1)}
	c.pending[57] = w
	c.recordPacketControl(a, simulatorEcho)
	if w.terminal != terminalAccepted {
		t.Fatalf("valid SimRadio text echo classified as %v", w.terminal)
	}

	wrongPort, err := proto.Marshal(&mesh.Compressed{Portnum: mesh.PortNum_POSITION_APP})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{name: "malformed", payload: []byte{0xff}},
		{name: "wrong embedded port", payload: wrongPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uint32(58)
			p := textPacket(a.ownNode, broadcastNode, id, 0, 0, "")
			p.SetDecoded(&mesh.Data{Portnum: mesh.PortNum_SIMULATOR_APP, Payload: tc.payload})
			waiter := &acceptance{id: id, destination: broadcastNode, attempt: a, queueOK: true, wake: make(chan struct{}, 1)}
			c.pending[id] = waiter
			c.recordPacketControl(a, p)
			if waiter.terminal != terminalPending || waiter.echoOK {
				t.Fatalf("invalid SimRadio echo accepted: %+v", waiter)
			}
		})
	}
}

func TestHopsAwayAndBaseLength(t *testing.T) {
	for _, tc := range []struct {
		start, limit uint32
		want         int
	}{{3, 3, 0}, {3, 1, 2}, {0, 0, -1}, {1, 2, -1}} {
		if got := hopsAway(tc.start, tc.limit); got != tc.want {
			t.Errorf("hopsAway(%d,%d)=%d, want %d", tc.start, tc.limit, got, tc.want)
		}
	}
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	if c.MaxMessageLength() != 0 {
		t.Fatalf("generic max message length=%d", c.MaxMessageLength())
	}
	if !strings.Contains(nodeID(1), "00000001") {
		t.Fatal("node ID is not canonical")
	}
}
