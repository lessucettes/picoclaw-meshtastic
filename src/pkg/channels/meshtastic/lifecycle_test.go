// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

type fakeTransport struct {
	opened atomic.Int32
	closed atomic.Int32
	once   sync.Once
	recv   chan *mesh.FromRadio
	done   chan struct{}
	mu     sync.Mutex
	sent   []*mesh.ToRadio
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{recv: make(chan *mesh.FromRadio, 8), done: make(chan struct{})}
}
func (f *fakeTransport) Open(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.opened.Add(1)
	return nil
}
func (f *fakeTransport) SendToRadio(ctx context.Context, m *mesh.ToRadio) error {
	if err := ctx.Err(); err != nil {
		return sendErr(err, false)
	}
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	return nil
}
func (f *fakeTransport) ReceiveFromRadio(ctx context.Context) (*mesh.FromRadio, error) {
	select {
	case m := <-f.recv:
		return m, nil
	case <-f.done:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (f *fakeTransport) Close() error {
	f.once.Do(func() { f.closed.Add(1); close(f.done) })
	return nil
}

func radioMyInfo(node uint32) *mesh.FromRadio {
	f := &mesh.FromRadio{}
	f.SetMyInfo(&mesh.MyNodeInfo{MyNodeNum: node})
	return f
}
func radioComplete(id uint32) *mesh.FromRadio {
	f := &mesh.FromRadio{}
	f.SetConfigCompleteId(id)
	return f
}
func radioChannel(index int32, role mesh.Channel_Role) *mesh.FromRadio {
	f := &mesh.FromRadio{}
	f.SetChannel(&mesh.Channel{Index: index, Role: role})
	return f
}

func TestHandshakeReadinessAndMismatchedCompletion(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	a := &attemptState{ctx: context.Background(), configID: 44, channels: make(map[uint32]mesh.Channel_Role)}
	c.setAttempt(a, false)
	if err := c.processEnvelope(a, radioComplete(43)); err != nil || c.attemptReady(a) {
		t.Fatalf("stale completion changed readiness: err=%v", err)
	}
	if err := c.processEnvelope(a, radioMyInfo(0x12345678)); err != nil || c.attemptReady(a) {
		t.Fatalf("MyNodeInfo alone changed readiness: err=%v", err)
	}
	if err := c.processEnvelope(a, radioChannel(0, mesh.Channel_PRIMARY)); err != nil {
		t.Fatal(err)
	}
	if err := c.processEnvelope(a, radioComplete(44)); err != nil || !c.attemptReady(a) {
		t.Fatalf("matching pair did not become ready: err=%v", err)
	}

	c2, _ := newTestChannel(t, "mesh2", config.GroupTriggerConfig{})
	a2 := &attemptState{ctx: context.Background(), configID: 5, channels: make(map[uint32]mesh.Channel_Role)}
	c2.setAttempt(a2, false)
	if err := c2.processEnvelope(a2, radioComplete(5)); err == nil {
		t.Fatal("matching completion without MyNodeInfo was accepted")
	}
}

func TestLifecycleStartStopAndOneShot(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	f := newFakeTransport()
	c.newTransport = func() transport { return f }
	var id [4]byte
	binary.BigEndian.PutUint32(id[:], 99)
	c.random = bytes.NewReader(bytes.Repeat(id[:], 8))
	f.recv <- radioMyInfo(0x12345678)
	f.recv <- radioChannel(0, mesh.Channel_PRIMARY)
	f.recv <- radioComplete(99)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("repeated Start: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		c.stateMu.Lock()
		ready, changed := c.state.ready, c.state.changed
		c.stateMu.Unlock()
		if ready {
			break
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("channel did not become ready")
		}
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("repeated Stop: %v", err)
	}
	if f.opened.Load() != 1 || f.closed.Load() != 1 || c.IsRunning() {
		t.Fatalf("opened=%d closed=%d running=%v", f.opened.Load(), f.closed.Load(), c.IsRunning())
	}
	if err := c.Start(context.Background()); !errors.Is(err, channels.ErrNotRunning) {
		t.Fatalf("restart error=%v", err)
	}
}

func TestHandshakeTimeoutTracksIdleProgress(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	c.handshakeIdle = 30 * time.Millisecond
	f := newFakeTransport()
	a := &attemptState{ctx: context.Background(), configID: 44, channels: make(map[uint32]mesh.Channel_Role), t: f}
	c.runCtx = context.Background()
	a.ctx, a.cancel = context.WithCancel(c.runCtx)

	done := make(chan error, 1)
	go func() { done <- c.runAttempt(a) }()
	for _, envelope := range []*mesh.FromRadio{
		radioMyInfo(0x12345678),
		radioChannel(0, mesh.Channel_PRIMARY),
		{}, {}, {}, {},
		radioComplete(44),
	} {
		time.Sleep(15 * time.Millisecond)
		f.recv <- envelope
	}

	deadline := time.After(time.Second)
	for !c.attemptReady(a) {
		select {
		case <-deadline:
			t.Fatal("handshake did not remain alive while envelopes made progress")
		case err := <-done:
			t.Fatalf("handshake ended before completion: %v", err)
		case <-time.After(time.Millisecond):
		}
	}
	_ = f.Close()
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("runAttempt error after readiness = %v, want EOF", err)
	}
}

func TestHandshakeTimeoutStillExpiresWhenIdle(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	c.handshakeIdle = 20 * time.Millisecond
	f := newFakeTransport()
	a := &attemptState{ctx: context.Background(), configID: 44, channels: make(map[uint32]mesh.Channel_Role), t: f}
	c.runCtx = context.Background()
	a.ctx, a.cancel = context.WithCancel(c.runCtx)

	done := make(chan error, 1)
	go func() { done <- c.runAttempt(a) }()
	select {
	case err := <-done:
		if err == nil || err.Error() != "Meshtastic configuration handshake timed out" {
			t.Fatalf("runAttempt error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle handshake did not time out")
	}
}

func TestStopBeforeStartIsPermanent(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); !errors.Is(err, channels.ErrNotRunning) {
		t.Fatalf("Start after Stop error=%v", err)
	}
}

func TestParentCancellationEndsOneShotLifecycle(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	f := newFakeTransport()
	c.newTransport = func() transport { return f }
	var id [4]byte
	binary.BigEndian.PutUint32(id[:], 8)
	c.random = bytes.NewReader(bytes.Repeat(id[:], 4))
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); !errors.Is(err, channels.ErrNotRunning) {
		t.Fatalf("restart after parent cancellation error=%v", err)
	}
}

func TestRandomNonzeroRetriesZeroAndFailure(t *testing.T) {
	c, _ := newTestChannel(t, "mesh", config.GroupTriggerConfig{})
	c.random = bytes.NewReader([]byte{0, 0, 0, 0, 0, 0, 0, 7})
	if got, err := c.randomNonzero(); err != nil || got != 7 {
		t.Fatalf("randomNonzero=(%d,%v)", got, err)
	}
	c.random = errReader{}
	if _, err := c.randomNonzero(); err == nil {
		t.Fatal("RNG failure was hidden")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("rng failed") }
