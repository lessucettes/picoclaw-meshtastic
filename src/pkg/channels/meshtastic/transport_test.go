// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
)

type fakePort struct {
	mu          sync.Mutex
	read        []byte
	written     []byte
	writeLimit  int
	drains      int
	closed      int
	timeouts    []time.Duration
	readBlocked chan struct{}
}

func (p *fakePort) Read(b []byte) (int, error) {
	p.mu.Lock()
	if len(p.read) != 0 {
		n := len(b)
		if n > len(p.read) {
			n = len(p.read)
		}
		copy(b, p.read[:n])
		p.read = p.read[n:]
		p.mu.Unlock()
		return n, nil
	}
	blocked := p.readBlocked
	p.mu.Unlock()
	if blocked != nil {
		<-blocked
		return 0, io.EOF
	}
	return 0, io.EOF
}
func (p *fakePort) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(b)
	if p.writeLimit > 0 && n > p.writeLimit {
		n = p.writeLimit
	}
	p.written = append(p.written, b[:n]...)
	return n, nil
}
func (p *fakePort) Drain() error { p.mu.Lock(); p.drains++; p.mu.Unlock(); return nil }
func (p *fakePort) SetReadTimeout(d time.Duration) error {
	p.mu.Lock()
	p.timeouts = append(p.timeouts, d)
	p.mu.Unlock()
	return nil
}
func (p *fakePort) Close() error {
	p.mu.Lock()
	p.closed++
	if p.readBlocked != nil {
		select {
		case <-p.readBlocked:
		default:
			close(p.readBlocked)
		}
	}
	p.mu.Unlock()
	return nil
}
func (*fakePort) SetMode(*serial.Mode) error                           { return nil }
func (*fakePort) ResetInputBuffer() error                              { return nil }
func (*fakePort) ResetOutputBuffer() error                             { return nil }
func (*fakePort) SetDTR(bool) error                                    { return nil }
func (*fakePort) SetRTS(bool) error                                    { return nil }
func (*fakePort) GetModemStatusBits() (*serial.ModemStatusBits, error) { return nil, nil }
func (*fakePort) Break(time.Duration) error                            { return nil }

func serialFrame(t *testing.T, msg *mesh.FromRadio) []byte {
	t.Helper()
	b, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, len(b)+4)
	frame[0], frame[1] = 0x94, 0xc3
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(b)))
	copy(frame[4:], b)
	return frame
}

func TestSerialOpenPreambleAndFraming(t *testing.T) {
	p := &fakePort{writeLimit: 3}
	st := newSerialTransport("test")
	st.opener = func(name string, mode *serial.Mode) (serial.Port, error) {
		if name != "test" || mode.BaudRate != 115200 {
			t.Fatalf("open(%q, %d)", name, mode.BaudRate)
		}
		return p, nil
	}
	if err := st.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.written) != 32 || p.drains != 1 || !bytes.Equal(p.written, bytes.Repeat([]byte{0xc3}, 32)) {
		t.Fatalf("preamble len=%d drains=%d", len(p.written), p.drains)
	}
	p.written = nil
	m := &mesh.ToRadio{}
	m.SetWantConfigId(42)
	if err := st.SendToRadio(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if p.drains != 1 || len(p.written) < 5 || !bytes.Equal(p.written[:2], []byte{0x94, 0xc3}) {
		t.Fatalf("bad framed write %x drains=%d", p.written, p.drains)
	}
	if int(binary.BigEndian.Uint16(p.written[2:4])) != len(p.written)-4 {
		t.Fatal("bad frame length")
	}
}

func TestSerialReceiveGarbageFalseMagicAndOversizeResync(t *testing.T) {
	want := &mesh.FromRadio{}
	want.SetConfigCompleteId(123)
	valid := serialFrame(t, want)
	input := append([]byte{1, 2, 0x94, 0x94, 0xc3, 0x02, 0x01}, valid...)
	p := &fakePort{read: input}
	st := newSerialTransport("test")
	st.port = p
	got, err := st.ReceiveFromRadio(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.GetConfigCompleteId() != 123 {
		t.Fatalf("got config ID %d", got.GetConfigCompleteId())
	}
	if len(p.timeouts) == 0 || p.timeouts[len(p.timeouts)-1] != serial.NoTimeout {
		t.Fatalf("read timeout was not reset: %v", p.timeouts)
	}
}

func TestSerialCancellationAfterPartialWriteIsAmbiguous(t *testing.T) {
	p := &fakePort{writeLimit: 1}
	st := newSerialTransport("test")
	st.port = p
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &mesh.ToRadio{}
	m.SetWantConfigId(1)
	err := st.SendToRadio(ctx, m)
	if err == nil || !definitelyZeroBytes(err) {
		t.Fatalf("pre-write cancellation=%v, zero=%v", err, definitelyZeroBytes(err))
	}
}

func TestSerialOpenCancellationRaceClosesReturnedPort(t *testing.T) {
	p := &fakePort{}
	st := newSerialTransport("test")
	ctx, cancel := context.WithCancel(context.Background())
	st.opener = func(string, *serial.Mode) (serial.Port, error) { cancel(); return p, nil }
	if err := st.Open(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open error=%v", err)
	}
	if p.closed != 1 || st.port != nil {
		t.Fatalf("closed=%d published=%v", p.closed, st.port != nil)
	}
}

func TestHTTPTransportMethodsLimitsAndRedirects(t *testing.T) {
	var gets, puts, active, maxActive atomic.Int32
	fr := &mesh.FromRadio{}
	fr.SetConfigCompleteId(77)
	frWire, _ := proto.Marshal(fr)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/toradio":
			puts.Add(1)
			if r.Method != http.MethodPut {
				t.Errorf("method=%s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/fromradio":
			if r.Method != http.MethodGet || r.URL.Query().Get("all") != "false" {
				t.Errorf("GET %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			n := active.Add(1)
			if n > maxActive.Load() {
				maxActive.Store(n)
			}
			gets.Add(1)
			active.Add(-1)
			_, _ = w.Write(frWire)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ht := newHTTPTransport(strings.TrimPrefix(server.URL, "http://"))
	if err := ht.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr := &mesh.ToRadio{}
	tr.SetWantConfigId(5)
	if err := ht.SendToRadio(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	got, err := ht.ReceiveFromRadio(context.Background())
	if err != nil || got.GetConfigCompleteId() != 77 {
		t.Fatalf("receive=%v err=%v", got, err)
	}
	if gets.Load() != 1 || puts.Load() != 1 || maxActive.Load() != 1 {
		t.Fatalf("gets=%d puts=%d maxActive=%d", gets.Load(), puts.Load(), maxActive.Load())
	}
	if err := ht.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ht.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPTransportRejectsMalformedAndOversizedBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"malformed", []byte{0xff}},
		{"oversized", bytes.Repeat([]byte{'x'}, maxEnvelopeBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(tc.body) }))
			defer s.Close()
			ht := newHTTPTransport(strings.TrimPrefix(s.URL, "http://"))
			if err := ht.Open(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := ht.ReceiveFromRadio(context.Background()); err == nil {
				t.Fatal("expected protocol error")
			}
		})
	}
}

func TestHTTPTransportCloseCancelsActiveReceive(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ht := newHTTPTransport(strings.TrimPrefix(server.URL, "http://"))
	if err := ht.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ht.ReceiveFromRadio(context.Background()); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("GET did not start")
	}
	if err := ht.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Receive returned nil after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Receive")
	}
}
