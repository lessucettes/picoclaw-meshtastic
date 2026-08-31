// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

const (
	maxEnvelopeBytes  = 512
	heartbeatInterval = 300 * time.Second
	httpPollInterval  = 3 * time.Second
)

type transport interface {
	Open(context.Context) error
	SendToRadio(context.Context, *mesh.ToRadio) error
	ReceiveFromRadio(context.Context) (*mesh.FromRadio, error)
	Close() error
}

// transportSendError preserves the only retry fact the channel may rely on:
// whether no request/frame byte was written.
type transportSendError struct {
	err   error
	wrote bool
}

func (e *transportSendError) Error() string { return e.err.Error() }
func (e *transportSendError) Unwrap() error { return e.err }

func definitelyZeroBytes(err error) bool {
	var se *transportSendError
	return errors.As(err, &se) && !se.wrote
}

func sendErr(err error, wrote bool) error {
	if err == nil {
		return nil
	}
	return &transportSendError{err: err, wrote: wrote}
}

func waitContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type closeState struct {
	mu     sync.Mutex
	closed bool
}

func (s *closeState) markClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.closed = true
	return true
}

func envelopeTooLarge(kind string, n int) error {
	return fmt.Errorf("meshtastic %s envelope is %d bytes, limit is %d", kind, n, maxEnvelopeBytes)
}
