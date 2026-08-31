// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"context"
	"sync"
	"time"
)

// idleWatchdog expires when no progress is reported for the configured interval.
type idleWatchdog struct {
	progress chan struct{}
	expired  chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func startIdleWatchdog(ctx context.Context, timeout time.Duration, expire func()) *idleWatchdog {
	w := &idleWatchdog{
		progress: make(chan struct{}),
		expired:  make(chan struct{}),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-w.progress:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				close(w.expired)
				expire()
				return
			}
		}
	}()
	return w
}

func (w *idleWatchdog) Reset(ctx context.Context) bool {
	select {
	case w.progress <- struct{}{}:
		return true
	case <-w.expired:
		return false
	case <-w.done:
		return false
	case <-ctx.Done():
		return false
	}
}

func (w *idleWatchdog) Expired() bool {
	select {
	case <-w.expired:
		return true
	default:
		return false
	}
}

func (w *idleWatchdog) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}
