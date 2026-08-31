// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func (c *Channel) processEnvelope(a *attemptState, fr *mesh.FromRadio) error {
	if my := fr.GetMyInfo(); my != nil {
		a.ownNode = my.GetMyNodeNum()
		a.gotMyInfo = a.ownNode != 0
	}
	if ni := fr.GetNodeInfo(); ni != nil && a.ownNode != 0 && ni.GetNum() == a.ownNode && ni.GetUser() != nil {
		a.shortName = ni.GetUser().GetShortName()
	}
	if ch := fr.GetChannel(); ch != nil && ch.GetIndex() >= 0 && ch.GetIndex() <= 7 {
		a.channels[uint32(ch.GetIndex())] = ch.GetRole()
		logger.DebugCF("meshtastic", "configuration channel received", map[string]any{
			"channel": c.Name(), "index": ch.GetIndex(), "role": ch.GetRole().String(),
		})
	}
	if _, ok := fr.GetPayloadVariant().(*mesh.FromRadio_ConfigCompleteId); ok {
		if fr.GetConfigCompleteId() == a.configID {
			if !a.gotMyInfo {
				return fmt.Errorf("configuration completed without MyNodeInfo")
			}
			c.publishReady(a)
		}
	}
	if qs := fr.GetQueueStatus(); qs != nil {
		c.recordQueueStatus(qs)
	}
	if cn := fr.GetClientNotification(); cn != nil {
		c.recordNotification(cn)
	}
	if p := fr.GetPacket(); p != nil {
		c.recordPacketControl(a, p)
		c.handleInboundPacket(a, p)
	}
	return nil
}

func (c *Channel) recordQueueStatus(qs *mesh.QueueStatus) {
	now := c.now()
	c.pendingMu.Lock()
	c.queueFree, c.queueKnown = qs.GetFree(), true
	close(c.queueChanged)
	c.queueChanged = make(chan struct{})
	if id := qs.GetMeshPacketId(); id != 0 {
		if a := c.pending[id]; a != nil && a.terminal == terminalPending {
			switch {
			case qs.GetRes() == 0:
				a.queueOK, a.queueAt = true, now
				if a.echoOK {
					c.finishLocked(a, terminalAccepted, "", now)
				} else {
					a.signal()
				}
			case qs.GetRes() == 32 && qs.GetFree() == 0:
				c.finishLocked(a, terminalQueueFull, "TX queue full", now)
			default:
				c.finishLocked(a, terminalPermanent, fmt.Sprintf("QueueStatus result %d", qs.GetRes()), now)
			}
		}
	}
	c.pendingMu.Unlock()
}

func (c *Channel) recordPacketControl(a *attemptState, p *mesh.MeshPacket) {
	from := p.GetFrom()
	if from != 0 && from != a.ownNode {
		return
	}
	if d := p.GetDecoded(); d != nil && d.GetPortnum() == mesh.PortNum_ROUTING_APP {
		requestID := d.GetRequestId()
		if requestID == 0 {
			return
		}
		var routing mesh.Routing
		if proto.Unmarshal(d.GetPayload(), &routing) != nil {
			return
		}
		reason := routing.GetErrorReason()
		if reason == mesh.Routing_NONE {
			return
		}
		c.pendingMu.Lock()
		w := c.pending[requestID]
		if w != nil && w.attempt == a && w.terminal == terminalPending {
			switch reason {
			case mesh.Routing_RATE_LIMIT_EXCEEDED:
				c.finishLocked(w, terminalRate, reason.String(), c.now())
			case mesh.Routing_DUTY_CYCLE_LIMIT:
				c.finishLocked(w, terminalDuty, reason.String(), c.now())
			default:
				c.finishLocked(w, terminalPermanent, reason.String(), c.now())
			}
		}
		c.pendingMu.Unlock()
		return
	}
	c.pendingMu.Lock()
	w := c.pending[p.GetId()]
	if w == nil || w.attempt != a || w.terminal != terminalPending {
		c.pendingMu.Unlock()
		return
	}
	validEcho := p.GetTo() == w.destination && isLocalTextEcho(p)
	if validEcho {
		w.echoOK = true
		if w.queueOK {
			c.finishLocked(w, terminalAccepted, "", c.now())
		}
	}
	c.pendingMu.Unlock()
}

func isLocalTextEcho(p *mesh.MeshPacket) bool {
	if len(p.GetEncrypted()) != 0 {
		return true
	}
	d := p.GetDecoded()
	if d == nil {
		return false
	}
	if d.GetPortnum() == mesh.PortNum_TEXT_MESSAGE_APP {
		return true
	}
	if d.GetPortnum() != mesh.PortNum_SIMULATOR_APP {
		return false
	}
	var compressed mesh.Compressed
	return proto.Unmarshal(d.GetPayload(), &compressed) == nil && compressed.GetPortnum() == mesh.PortNum_TEXT_MESSAGE_APP
}

func (c *Channel) finishLocked(a *acceptance, kind terminalKind, reason string, at time.Time) {
	if a.terminal != terminalPending {
		return
	}
	a.terminal, a.reason, a.at = kind, reason, at
	a.signal()
}

func (c *Channel) failPending(attempt *attemptState, kind terminalKind, reason string) {
	c.pendingMu.Lock()
	for _, a := range c.pending {
		if a.attempt == attempt {
			c.finishLocked(a, kind, reason, c.now())
		}
	}
	c.queueKnown = false
	close(c.queueChanged)
	c.queueChanged = make(chan struct{})
	c.pendingMu.Unlock()
}

func exactDutyMinutes(message string) (int, bool) {
	const prefix = "Duty cycle limit exceeded. You can send again in "
	const suffix = " mins"
	if !strings.HasPrefix(message, prefix) || !strings.HasSuffix(message, suffix) {
		return 0, false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(message, prefix), suffix)
	if middle == "" {
		return 0, false
	}
	for _, r := range middle {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(middle)
	if err != nil || n < 0 || n > 60 {
		return 0, false
	}
	return n, true
}

func (c *Channel) recordNotification(n *mesh.ClientNotification) {
	if n.ReplyId == nil || n.GetLevel() != mesh.LogRecord_WARNING {
		return
	}
	minutes, ok := exactDutyMinutes(n.GetMessage())
	if !ok {
		return
	}
	c.pendingMu.Lock()
	id := n.GetReplyId()
	if a := c.pending[id]; a != nil && a.terminal == terminalPending {
		v := minutes
		a.dutyMinutes = &v
	}
	if c.dutyID == id {
		v := minutes
		c.dutyMinutes = &v
		close(c.dutyChanged)
		c.dutyChanged = make(chan struct{})
	}
	c.pendingMu.Unlock()
}

func (c *Channel) handleInboundPacket(a *attemptState, p *mesh.MeshPacket) {
	d := p.GetDecoded()
	if d == nil || d.GetPortnum() != mesh.PortNum_TEXT_MESSAGE_APP || d.GetEmoji() != 0 {
		return
	}
	ready := c.attemptReady(a)
	if !ready {
		logger.WarnCF("meshtastic", "dropping text before readiness", map[string]any{"channel": c.Name(), "from": nodeID(p.GetFrom()), "packet_id": p.GetId(), "channel_index": p.GetChannel()})
		return
	}
	if !utf8.Valid(d.GetPayload()) {
		logger.WarnCF("meshtastic", "dropping invalid UTF-8 text", map[string]any{"channel": c.Name(), "packet_id": p.GetId()})
		return
	}
	if p.GetId() == 0 {
		logger.WarnCF("meshtastic", "dropping zero packet ID", map[string]any{"channel": c.Name()})
		return
	}
	from := p.GetFrom()
	if from == 0 {
		from = a.ownNode
	}
	if from == a.ownNode {
		return
	}
	direct, group := p.GetTo() == a.ownNode, p.GetTo() == broadcastNode
	if !direct && !group {
		return
	}
	if !usableRole(a.channels[p.GetChannel()]) {
		logger.WarnCF("meshtastic", "dropping packet on unavailable device channel", map[string]any{"channel": c.Name(), "index": p.GetChannel()})
		return
	}
	if group && !containsIndex(c.indices, p.GetChannel()) {
		return
	}

	id := nodeID(from)
	sender := bus.SenderInfo{Platform: "meshtastic", PlatformID: id, CanonicalID: "meshtastic:" + id, DisplayName: id}
	if !c.IsAllowedSender(sender) {
		return
	}
	original := string(d.GetPayload())
	native := group && d.GetReplyId() != 0 && c.sentBot.contains(sentKey{id: d.GetReplyId(), channel: p.GetChannel()}, c.now())
	_, _, mentioned := mentionAt(original, c.snapshotShortName(a))
	prefix, prefixMatched := c.matchPrefix(original)
	trigger := ""
	if direct {
		trigger = "dm"
	} else {
		switch {
		case native:
			trigger = "native_reply"
		case mentioned:
			trigger = "mention"
		case !c.bc.GroupTrigger.MentionOnly && prefixMatched:
			trigger = "prefix"
		case !c.bc.GroupTrigger.MentionOnly && len(c.prefixes) == 0:
			trigger = "group"
		default:
			return
		}
	}
	prompt := original
	if prefixMatched {
		prompt = strings.TrimPrefix(prompt, prefix)
	}
	if mentioned {
		// Re-locate after optional prefix removal to remove exactly the first mention.
		if s, e, ok := mentionAt(prompt, c.snapshotShortName(a)); ok {
			prompt = prompt[:s] + prompt[e:]
		}
	}
	prompt = strings.Join(strings.Fields(prompt), " ")
	if prompt == "" {
		return
	}
	if c.inboundDedup.seenOrAdd(inboundKey{from: from, id: p.GetId()}, c.now()) {
		return
	}

	chatID, chatType := id, "direct"
	if group {
		chatID, chatType = "channel:"+strconv.FormatUint(uint64(p.GetChannel()), 10), "group"
	}
	raw := map[string]string{
		"meshtastic_from": id, "meshtastic_to": nodeID(p.GetTo()),
		"meshtastic_packet_id":         strconv.FormatUint(uint64(p.GetId()), 10),
		"meshtastic_channel_index":     strconv.FormatUint(uint64(p.GetChannel()), 10),
		"meshtastic_reply_id":          strconv.FormatUint(uint64(d.GetReplyId()), 10),
		"meshtastic_conversation_type": chatType, "meshtastic_trigger": trigger,
		"meshtastic_rx_snr":        strconv.FormatFloat(float64(p.GetRxSnr()), 'f', -1, 32),
		"meshtastic_hops_away":     strconv.Itoa(hopsAway(p.GetHopStart(), p.GetHopLimit())),
		"meshtastic_via_mqtt":      strconv.FormatBool(p.GetViaMqtt()),
		"meshtastic_pki_encrypted": strconv.FormatBool(p.GetPkiEncrypted()),
	}
	if p.HasRxRssi() {
		raw["meshtastic_rx_rssi"] = strconv.FormatInt(int64(p.GetRxRssi()), 10)
	}
	ctx := bus.InboundContext{Channel: c.Name(), ChatID: chatID, ChatType: chatType, SenderID: sender.CanonicalID,
		MessageID: strconv.FormatUint(uint64(p.GetId()), 10), Mentioned: mentioned, Raw: raw}
	if d.GetReplyId() != 0 {
		ctx.ReplyToMessageID = strconv.FormatUint(uint64(d.GetReplyId()), 10)
	}
	in := inboundPrompt{chatID: chatID, content: prompt, ctx: ctx, sender: sender}
	select {
	case c.inbound <- in:
	default:
		n := c.dropped.Add(1)
		logger.WarnCF("meshtastic", "inbound prompt queue full", map[string]any{"channel": c.Name(), "sender": id, "chat_id": chatID, "packet_id": p.GetId(), "capacity": inboundQueueSize, "dropped": n})
	}
}

func (c *Channel) snapshotShortName(a *attemptState) string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.state.attempt != a {
		return ""
	}
	return c.state.shortName
}

func (c *Channel) matchPrefix(s string) (string, bool) {
	for _, p := range c.prefixes {
		if strings.HasPrefix(s, p) {
			return p, true
		}
	}
	return "", false
}

func nodeID(n uint32) string { return fmt.Sprintf("!%08x", n) }
func containsIndex(indices []uint32, n uint32) bool {
	for _, v := range indices {
		if v == n {
			return true
		}
	}
	return false
}
func hopsAway(start, limit uint32) int {
	if start == 0 || limit > start {
		return -1
	}
	return int(start - limit)
}
