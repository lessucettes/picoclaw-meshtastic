// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type routeSpec struct {
	direct      bool
	destination uint32
	channel     uint32
	proactiveDM bool
}

type readySnapshot struct {
	attempt  *attemptState
	ownNode  uint32
	channels map[uint32]mesh.Channel_Role
}

func (c *Channel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	if !utf8.ValidString(msg.Content) {
		return nil, fmt.Errorf("invalid UTF-8: %w", channels.ErrSendFailed)
	}
	route, err := c.parseRoute(msg)
	if err != nil {
		return nil, err
	}
	replyID, err := parseReplyID(msg)
	if err != nil {
		return nil, err
	}
	count, total, oversized, err := chunkCount(msg.Content, c.textChunkBytes, replyID, route.direct)
	if err != nil {
		return nil, fmt.Errorf("chunk message: %w: %w", err, channels.ErrSendFailed)
	}
	if count == 0 {
		return []string{}, nil
	}
	text := msg.Content
	if oversized {
		logger.WarnCF("meshtastic", "replacing oversized response", map[string]any{"channel": c.Name()})
		text, total = oversizedNotice, 1
	}

	c.sendGate.Lock()
	defer c.sendGate.Unlock()
	ids := make([]string, 0, total)
	_, scanErr := scanChunks(text, c.textChunkBytes, total, maxTextChunks, replyID, route.direct, func(payload string) error {
		id, sendErr := c.sendPhysical(ctx, route, replyID, payload)
		if sendErr != nil {
			return sendErr
		}
		ids = append(ids, strconv.FormatUint(uint64(id), 10))
		return nil
	})
	if scanErr != nil {
		if len(ids) != 0 {
			return ids, fmt.Errorf("partial Meshtastic send after %d chunks: %w: %w", len(ids), scanErr, channels.ErrSendFailed)
		}
		if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) || errors.Is(scanErr, channels.ErrSendFailed) {
			return nil, scanErr
		}
		return nil, fmt.Errorf("scan Meshtastic chunks: %w: %w", scanErr, channels.ErrSendFailed)
	}
	return ids, nil
}

func parseReplyID(msg bus.OutboundMessage) (uint32, error) {
	s := strings.TrimSpace(msg.Context.MessageID)
	if s == "" {
		s = strings.TrimSpace(msg.ReplyToMessageID)
	}
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("invalid Meshtastic reply ID %q: %w", s, channels.ErrSendFailed)
	}
	return uint32(v), nil
}

func (c *Channel) parseRoute(msg bus.OutboundMessage) (routeSpec, error) {
	chat := msg.ChatID
	if strings.TrimSpace(chat) != chat {
		return routeSpec{}, fmt.Errorf("invalid Meshtastic ChatID %q: %w", chat, channels.ErrSendFailed)
	}
	if len(chat) == 9 && chat[0] == '!' {
		for i := 1; i < len(chat); i++ {
			if !((chat[i] >= '0' && chat[i] <= '9') || (chat[i] >= 'a' && chat[i] <= 'f')) {
				return routeSpec{}, fmt.Errorf("invalid direct ChatID %q: %w", chat, channels.ErrSendFailed)
			}
		}
		v, err := strconv.ParseUint(chat[1:], 16, 32)
		if err != nil || v == 0 || uint32(v) == broadcastNode {
			return routeSpec{}, fmt.Errorf("invalid direct ChatID %q: %w", chat, channels.ErrSendFailed)
		}
		r := routeSpec{direct: true, destination: uint32(v)}
		if raw := msg.Context.Raw; raw != nil && raw["meshtastic_channel_index"] != "" {
			idx, err := strconv.ParseUint(raw["meshtastic_channel_index"], 10, 32)
			if err != nil || idx > 7 {
				return routeSpec{}, fmt.Errorf("invalid inbound Meshtastic channel index: %w", channels.ErrSendFailed)
			}
			r.channel = uint32(idx)
		} else {
			if len(c.indices) == 0 {
				return routeSpec{}, fmt.Errorf("no proactive DM channel: %w", channels.ErrSendFailed)
			}
			r.channel, r.proactiveDM = c.indices[0], true
		}
		return r, nil
	}
	if strings.HasPrefix(chat, "channel:") {
		tail := strings.TrimPrefix(chat, "channel:")
		if tail == "" || strings.HasPrefix(tail, "+") || (len(tail) > 1 && tail[0] == '0') {
			return routeSpec{}, fmt.Errorf("invalid group ChatID %q: %w", chat, channels.ErrSendFailed)
		}
		idx, err := strconv.ParseUint(tail, 10, 32)
		if err != nil || idx > 7 {
			return routeSpec{}, fmt.Errorf("invalid group ChatID %q: %w", chat, channels.ErrSendFailed)
		}
		return routeSpec{destination: broadcastNode, channel: uint32(idx)}, nil
	}
	return routeSpec{}, fmt.Errorf("unsupported Meshtastic ChatID %q: %w", chat, channels.ErrSendFailed)
}

func (c *Channel) waitReady(ctx context.Context) (readySnapshot, error) {
	for {
		c.stateMu.Lock()
		if c.state.ready && c.state.attempt != nil {
			s := readySnapshot{attempt: c.state.attempt, ownNode: c.state.ownNode, channels: make(map[uint32]mesh.Channel_Role, len(c.state.channels))}
			for k, v := range c.state.channels {
				s.channels[k] = v
			}
			c.stateMu.Unlock()
			return s, nil
		}
		changed := c.state.changed
		c.stateMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return readySnapshot{}, ctx.Err()
		case <-c.runCtx.Done():
			return readySnapshot{}, channels.ErrNotRunning
		}
	}
}

func (c *Channel) waitPacing(ctx context.Context) error {
	if c.lastPacing.IsZero() {
		return nil
	}
	d := c.sendDelay - c.now().Sub(c.lastPacing)
	if d <= 0 {
		return nil
	}
	return waitContext(ctx, d)
}

func (c *Channel) sendPhysical(ctx context.Context, route routeSpec, replyID uint32, payload string) (uint32, error) {
	var previous uint32
	notBefore := time.Time{}
	for {
		if !notBefore.IsZero() {
			if d := notBefore.Sub(c.now()); d > 0 {
				if err := waitContext(ctx, d); err != nil {
					return 0, err
				}
			}
		}
		s, err := c.waitReady(ctx)
		if err != nil {
			return 0, err
		}
		if !usableRole(s.channels[route.channel]) {
			return 0, fmt.Errorf("device channel %d is unavailable: %w", route.channel, channels.ErrSendFailed)
		}
		if err := c.waitPacing(ctx); err != nil {
			return 0, err
		}
		id, err := c.newPacketID(previous)
		if err != nil {
			return 0, fmt.Errorf("packet ID generation: %w: %w", err, channels.ErrSendFailed)
		}
		toRadio, err := buildTextEnvelope(s.ownNode, route, id, replyID, payload)
		if err != nil {
			return 0, err
		}
		wire, err := proto.Marshal(toRadio)
		if err != nil || len(wire) > maxEnvelopeBytes {
			if err == nil {
				err = envelopeTooLarge("ToRadio", len(wire))
			}
			return 0, fmt.Errorf("serialize text packet: %w: %w", err, channels.ErrSendFailed)
		}

		submitCtx, submitCancel := context.WithTimeout(ctx, acceptanceTimeout)
		w := &acceptance{id: id, destination: route.destination, attempt: s.attempt, terminal: terminalPending, wake: make(chan struct{}, 1)}
		c.pendingMu.Lock()
		if _, collision := c.pending[id]; collision {
			c.pendingMu.Unlock()
			submitCancel()
			previous = id
			continue
		}
		c.pending[id] = w
		c.pendingMu.Unlock()

		opCtx, opCancel := context.WithCancel(submitCtx)
		stopAttemptCancel := context.AfterFunc(s.attempt.ctx, opCancel)
		sendCallErr := s.attempt.t.SendToRadio(opCtx, toRadio)
		stopAttemptCancel()
		opCancel()

		if sendCallErr != nil {
			kind := c.removeAcceptance(w, terminalAmbiguous, sendCallErr.Error())
			s.attempt.cancel()
			submitCancel()
			if kind == terminalAccepted {
				return c.commitAccepted(route, id), nil
			}
			if definitelyZeroBytes(sendCallErr) && ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if definitelyZeroBytes(sendCallErr) && ctx.Err() == nil {
				previous = id
				continue
			}
			return 0, fmt.Errorf("ambiguous Meshtastic submission: %w: %w", sendCallErr, channels.ErrSendFailed)
		}

		kind, reason, at, duty := c.waitAcceptance(submitCtx, w)
		submitCancel()
		c.removeExact(w)
		switch kind {
		case terminalAccepted:
			logger.DebugCF("meshtastic", "local send accepted", map[string]any{
				"channel": c.Name(), "packet_id": id, "queue_status": w.queueOK,
				"local_echo": w.echoOK, "quiet_rejection_window": !w.echoOK,
			})
			return c.commitAccepted(route, id), nil
		case terminalQueueFull:
			c.lastPacing, previous = at, id
			if err := c.waitQueueRecovery(ctx, at.Add(c.sendDelay)); err != nil {
				return 0, err
			}
			notBefore = time.Time{}
			continue
		case terminalRate:
			c.lastPacing, previous, notBefore = at, id, at.Add(c.sendDelay)
			continue
		case terminalDuty:
			c.lastPacing, previous = at, id
			if err := c.waitDutyRecovery(ctx, id, at, duty); err != nil {
				return 0, err
			}
			notBefore = time.Time{}
			continue
		case terminalPermanent:
			return 0, fmt.Errorf("local Meshtastic rejection: %s: %w", reason, channels.ErrSendFailed)
		default:
			if ctx.Err() != nil {
				return 0, fmt.Errorf("ambiguous submission after cancellation: %w: %w", ctx.Err(), channels.ErrSendFailed)
			}
			return 0, fmt.Errorf("ambiguous Meshtastic submission: %s: %w", reason, channels.ErrSendFailed)
		}
	}
}

func buildTextEnvelope(own uint32, route routeSpec, id, replyID uint32, payload string) (*mesh.ToRadio, error) {
	if !fitsPhysical(payload, replyID, route.direct) {
		return nil, fmt.Errorf("text payload does not fit Meshtastic frame: %w", channels.ErrSendFailed)
	}
	d := &mesh.Data{Portnum: mesh.PortNum_TEXT_MESSAGE_APP, Payload: []byte(payload), ReplyId: replyID, WantResponse: false}
	p := &mesh.MeshPacket{From: own, To: route.destination, Channel: route.channel, Id: id, WantAck: true, HopLimit: 0, Priority: mesh.MeshPacket_UNSET}
	p.SetDecoded(d)
	t := &mesh.ToRadio{}
	t.SetPacket(p)
	return t, nil
}

func (c *Channel) newPacketID(previous uint32) (uint32, error) {
	for {
		id, err := c.randomNonzero()
		if err != nil {
			return 0, err
		}
		if id == previous {
			continue
		}
		c.pendingMu.Lock()
		_, live := c.pending[id]
		c.pendingMu.Unlock()
		if live || c.sentBot.any(func(k sentKey) bool { return k.id == id }, c.now()) {
			continue
		}
		return id, nil
	}
}

func (c *Channel) removeAcceptance(w *acceptance, fallback terminalKind, reason string) terminalKind {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if w.terminal == terminalPending {
		c.finishLocked(w, fallback, reason, c.now())
	}
	delete(c.pending, w.id)
	return w.terminal
}

func (c *Channel) removeExact(w *acceptance) {
	c.pendingMu.Lock()
	if c.pending[w.id] == w {
		delete(c.pending, w.id)
	}
	c.pendingMu.Unlock()
}

func (c *Channel) waitAcceptance(ctx context.Context, w *acceptance) (terminalKind, string, time.Time, *int) {
	for {
		c.pendingMu.Lock()
		kind, reason, at, duty := w.terminal, w.reason, w.at, w.dutyMinutes
		queueOK, queueAt := w.queueOK, w.queueAt
		c.pendingMu.Unlock()
		if kind != terminalPending {
			return kind, reason, at, duty
		}
		var quiet <-chan time.Time
		var timer *time.Timer
		if queueOK {
			delay := queueAt.Add(c.rejectionGrace).Sub(c.now())
			if delay <= 0 {
				c.pendingMu.Lock()
				if w.terminal == terminalPending && w.queueOK {
					c.finishLocked(w, terminalAccepted, "", w.queueAt)
				}
				c.pendingMu.Unlock()
				continue
			}
			timer = time.NewTimer(delay)
			quiet = timer.C
		}
		select {
		case <-w.wake:
			if timer != nil {
				timer.Stop()
			}
		case <-quiet:
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			c.pendingMu.Lock()
			c.finishLocked(w, terminalAmbiguous, ctx.Err().Error(), c.now())
			c.pendingMu.Unlock()
		case <-w.attempt.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			c.pendingMu.Lock()
			c.finishLocked(w, terminalAmbiguous, "connection attempt ended", c.now())
			c.pendingMu.Unlock()
		}
	}
}

func (c *Channel) commitAccepted(route routeSpec, id uint32) uint32 {
	c.lastPacing = c.now()
	if !route.direct {
		c.sentBot.add(sentKey{id: id, channel: route.channel}, c.lastPacing)
	}
	return id
}

func (c *Channel) waitQueueRecovery(ctx context.Context, pacingAt time.Time) error {
	for {
		c.pendingMu.Lock()
		free, known, changed := c.queueFree, c.queueKnown, c.queueChanged
		c.pendingMu.Unlock()
		d := pacingAt.Sub(c.now())
		if known && free > 0 && d <= 0 {
			return nil
		}
		var timer <-chan time.Time
		var tm *time.Timer
		if d > 0 {
			tm = time.NewTimer(d)
			timer = tm.C
		}
		select {
		case <-changed:
		case <-timer:
		case <-ctx.Done():
			if tm != nil {
				tm.Stop()
			}
			return ctx.Err()
		case <-c.runCtx.Done():
			if tm != nil {
				tm.Stop()
			}
			return channels.ErrNotRunning
		}
		if tm != nil {
			tm.Stop()
		}
	}
}

func (c *Channel) waitDutyRecovery(ctx context.Context, id uint32, rejectedAt time.Time, prior *int) error {
	c.pendingMu.Lock()
	c.dutyID, c.dutyMinutes = id, prior
	close(c.dutyChanged)
	c.dutyChanged = make(chan struct{})
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		if c.dutyID == id {
			c.dutyID, c.dutyMinutes = 0, nil
		}
		c.pendingMu.Unlock()
	}()
	for {
		c.pendingMu.Lock()
		mins, changed := c.dutyMinutes, c.dutyChanged
		c.pendingMu.Unlock()
		deadline := rejectedAt.Add(60 * time.Minute)
		if mins != nil {
			candidate := rejectedAt.Add(time.Duration(*mins) * time.Minute)
			if candidate.Before(deadline) {
				deadline = candidate
			}
		}
		if pacing := rejectedAt.Add(c.sendDelay); pacing.After(deadline) {
			deadline = pacing
		}
		if d := deadline.Sub(c.now()); d <= 0 {
			return nil
		} else {
			tm := time.NewTimer(d)
			select {
			case <-tm.C:
			case <-changed:
				if !tm.Stop() {
					select {
					case <-tm.C:
					default:
					}
				}
				continue
			case <-ctx.Done():
				tm.Stop()
				return ctx.Err()
			case <-c.runCtx.Done():
				tm.Stop()
				return channels.ErrNotRunning
			}
		}
	}
}
