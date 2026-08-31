// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
)

const serialInterByteTimeout = 5 * time.Second

type serialPort interface {
	io.ReadWriteCloser
	Drain() error
	SetReadTimeout(time.Duration) error
}

type serialOpener func(string, *serial.Mode) (serial.Port, error)

type serialTransport struct {
	name   string
	opener serialOpener

	mu        sync.Mutex
	port      serialPort
	closed    bool
	writeMu   sync.Mutex
	lastWrite time.Time
}

func newSerialTransport(name string) *serialTransport {
	return &serialTransport{name: name, opener: serial.Open}
}

func (t *serialTransport) Open(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := t.opener(t.name, &serial.Mode{BaudRate: 115200})
	if err != nil {
		return fmt.Errorf("open serial %q: %w", t.name, err)
	}
	if err := ctx.Err(); err != nil {
		_ = p.Close()
		return err
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = p.Close()
		return context.Canceled
	}
	t.port = p
	t.mu.Unlock()

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	wake := make([]byte, 32)
	for i := range wake {
		wake[i] = 0xc3
	}
	if _, err := t.writeAll(ctx, p, wake); err != nil {
		return fmt.Errorf("serial wake preamble: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.Drain(); err != nil {
		return fmt.Errorf("drain serial wake preamble: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return waitContext(ctx, 100*time.Millisecond)
}

func (t *serialTransport) currentPort() (serialPort, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.port == nil {
		return nil, errors.New("serial transport is closed")
	}
	return t.port, nil
}

func (t *serialTransport) SendToRadio(ctx context.Context, msg *mesh.ToRadio) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return sendErr(fmt.Errorf("marshal ToRadio: %w", err), false)
	}
	if len(payload) > maxEnvelopeBytes {
		return sendErr(envelopeTooLarge("ToRadio", len(payload)), false)
	}
	frame := make([]byte, 4+len(payload))
	frame[0], frame[1] = 0x94, 0xc3
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(payload)))
	copy(frame[4:], payload)

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	p, err := t.currentPort()
	if err != nil {
		return sendErr(err, false)
	}
	n, err := t.writeAll(ctx, p, frame)
	if err != nil {
		return sendErr(err, n != 0)
	}
	t.lastWrite = time.Now()
	return nil
}

func (t *serialTransport) writeAll(ctx context.Context, p serialPort, b []byte) (int, error) {
	total := 0
	for len(b) != 0 {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := p.Write(b)
		total += n
		if err != nil {
			return total, err
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if n <= 0 {
			return total, io.ErrShortWrite
		}
		b = b[n:]
	}
	return total, nil
}

func (t *serialTransport) ReceiveFromRadio(ctx context.Context) (*mesh.FromRadio, error) {
	p, err := t.currentPort()
	if err != nil {
		return nil, err
	}
	const (
		seekFirst = iota
		seekSecond
		readLenHi
		readLenLo
		readBody
	)
	state := seekFirst
	var hi byte
	var body [maxEnvelopeBytes]byte
	want, have := 0, 0
	buf := []byte{0}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := p.Read(buf)
		if readErr != nil {
			return nil, readErr
		}
		if n == 0 {
			if state != seekFirst {
				state, have, want = seekFirst, 0, 0
				if err := p.SetReadTimeout(serial.NoTimeout); err != nil {
					return nil, err
				}
			}
			continue
		}
		if state == seekFirst {
			if buf[0] != 0x94 {
				continue
			}
			if err := p.SetReadTimeout(serialInterByteTimeout); err != nil {
				return nil, err
			}
			state = seekSecond
			continue
		} else if err := p.SetReadTimeout(serialInterByteTimeout); err != nil {
			return nil, err
		}
		b := buf[0]
		switch state {
		case seekSecond:
			if b == 0xc3 {
				state = readLenHi
			} else if b != 0x94 {
				state = seekFirst
			}
		case readLenHi:
			hi, state = b, readLenLo
		case readLenLo:
			want = int(binary.BigEndian.Uint16([]byte{hi, b}))
			if want > maxEnvelopeBytes {
				state, want, have = seekFirst, 0, 0
				if err := p.SetReadTimeout(serial.NoTimeout); err != nil {
					return nil, err
				}
				continue
			}
			if want == 0 {
				return nil, errors.New("empty FromRadio protobuf")
			}
			have, state = 0, readBody
		case readBody:
			body[have] = b
			have++
			if have == want {
				if err := p.SetReadTimeout(serial.NoTimeout); err != nil {
					return nil, err
				}
				var out mesh.FromRadio
				if err := proto.Unmarshal(body[:want], &out); err != nil {
					state, have, want = seekFirst, 0, 0
					continue
				}
				return &out, nil
			}
		}
	}
}

func (t *serialTransport) idleFor(now time.Time) time.Duration {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.lastWrite.IsZero() {
		return heartbeatInterval
	}
	return now.Sub(t.lastWrite)
}

func (t *serialTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	p := t.port
	t.port = nil
	t.mu.Unlock()
	if p != nil {
		return p.Close()
	}
	return nil
}
