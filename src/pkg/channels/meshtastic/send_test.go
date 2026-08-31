// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

type acceptingTransport struct {
	c         *Channel
	a         *attemptState
	echoFirst bool
	returnErr error
	mu        sync.Mutex
	submitted []*mesh.MeshPacket
}

func (*acceptingTransport) Open(context.Context) error { return nil }
func (t *acceptingTransport) SendToRadio(ctx context.Context, msg *mesh.ToRadio) error {
	if t.returnErr != nil {
		return t.returnErr
	}
	p := msg.GetPacket()
	t.mu.Lock()
	t.submitted = append(t.submitted, p)
	t.mu.Unlock()
	echo := &mesh.MeshPacket{From: t.a.ownNode, To: p.GetTo(), Id: p.GetId(), Channel: p.GetChannel()}
	echo.SetDecoded(&mesh.Data{Portnum: mesh.PortNum_TEXT_MESSAGE_APP, Payload: append([]byte(nil), p.GetDecoded().GetPayload()...)})
	status := &mesh.QueueStatus{MeshPacketId: p.GetId(), Res: 0, Free: 1}
	if t.echoFirst {
		t.c.recordPacketControl(t.a, echo)
		t.c.recordQueueStatus(status)
	} else {
		t.c.recordQueueStatus(status)
		t.c.recordPacketControl(t.a, echo)
	}
	return ctx.Err()
}
func (*acceptingTransport) ReceiveFromRadio(context.Context) (*mesh.FromRadio, error) {
	return nil, io.EOF
}
func (*acceptingTransport) Close() error { return nil }

func prepareSendChannel(t *testing.T, echoFirst bool) (*Channel, *acceptingTransport) {
	t.Helper()
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	c.runCtx = context.Background()
	c.SetRunning(true)
	ctx, cancel := context.WithCancel(context.Background())
	a := &attemptState{ctx: ctx, cancel: cancel, ownNode: 0x01020304, gotMyInfo: true, channels: map[uint32]mesh.Channel_Role{0: mesh.Channel_PRIMARY}}
	ft := &acceptingTransport{c: c, a: a, echoFirst: echoFirst}
	a.t = ft
	c.setAttempt(a, false)
	c.publishReady(a)
	var id [4]byte
	binary.BigEndian.PutUint32(id[:], 1234)
	c.random = bytes.NewReader(bytes.Repeat(id[:], 16))
	return c, ft
}

func TestSendImmediateAcceptanceInEitherOrder(t *testing.T) {
	for _, echoFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "queue-first", true: "echo-first"}[echoFirst], func(t *testing.T) {
			c, ft := prepareSendChannel(t, echoFirst)
			ids, err := c.Send(context.Background(), bus.OutboundMessage{
				ChatID: "channel:0", Content: "hello mesh",
				Context: bus.InboundContext{MessageID: "77"},
			})
			if err != nil || len(ids) != 1 || ids[0] != "1234" {
				t.Fatalf("Send ids=%v err=%v", ids, err)
			}
			ft.mu.Lock()
			defer ft.mu.Unlock()
			if len(ft.submitted) != 1 {
				t.Fatalf("submitted=%d", len(ft.submitted))
			}
			p := ft.submitted[0]
			if p.GetId() != 1234 || p.GetTo() != broadcastNode || p.GetDecoded().GetReplyId() != 77 || string(p.GetDecoded().GetPayload()) != "hello mesh" {
				t.Fatalf("submitted packet=%+v data=%+v", p, p.GetDecoded())
			}
			if !c.sentBot.contains(sentKey{id: 1234, channel: 0}, c.now()) {
				t.Fatal("accepted group packet was not cached for native replies")
			}
			if len(c.pending) != 0 {
				t.Fatalf("pending waiter leaked: %d", len(c.pending))
			}
		})
	}
}

func TestSendCancellationBeforeTransportBytesReturnsContextError(t *testing.T) {
	c, ft := prepareSendChannel(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ft.returnErr = sendErr(context.Canceled, false)
	_, err := c.Send(ctx, bus.OutboundMessage{ChatID: "channel:0", Content: "hello"})
	if !errors.Is(err, context.Canceled) || errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("Send error=%v", err)
	}
}
