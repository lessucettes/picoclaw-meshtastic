// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	reconnectDelay    = 10 * time.Second
	handshakeTimeout  = 30 * time.Second
	acceptanceTimeout = 60 * time.Second
	localRejectGrace  = httpPollInterval + time.Second
	inboundQueueSize  = 64
	broadcastNode     = ^uint32(0)
	oversizedNotice   = "The response ended up being too long."
)

type transportFactory func() transport

type channelState struct {
	attempt   *attemptState
	ready     bool
	ownNode   uint32
	shortName string
	channels  map[uint32]mesh.Channel_Role
	changed   chan struct{}
}

type attemptState struct {
	ctx       context.Context
	cancel    context.CancelFunc
	t         transport
	configID  uint32
	gotMyInfo bool
	ownNode   uint32
	shortName string
	channels  map[uint32]mesh.Channel_Role
}

type inboundPrompt struct {
	chatID  string
	content string
	ctx     bus.InboundContext
	sender  bus.SenderInfo
}

type terminalKind uint8

const (
	terminalPending terminalKind = iota
	terminalAccepted
	terminalQueueFull
	terminalRate
	terminalDuty
	terminalPermanent
	terminalAmbiguous
)

type acceptance struct {
	id          uint32
	destination uint32
	attempt     *attemptState
	queueOK     bool
	echoOK      bool
	queueAt     time.Time
	terminal    terminalKind
	reason      string
	at          time.Time
	wake        chan struct{}
	dutyMinutes *int
}

func (a *acceptance) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

type Channel struct {
	*channels.BaseChannel
	bc             *config.Channel
	cfg            config.MeshtasticSettings
	indices        []uint32
	textChunkBytes int
	sendDelay      time.Duration
	handshakeIdle  time.Duration
	rejectionGrace time.Duration
	newTransport   transportFactory
	random         ioReader
	now            func() time.Time

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
	runCtx      context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	stateMu sync.Mutex
	state   channelState

	pendingMu    sync.Mutex
	pending      map[uint32]*acceptance
	queueFree    uint32
	queueKnown   bool
	queueChanged chan struct{}
	dutyID       uint32
	dutyMinutes  *int
	dutyChanged  chan struct{}

	sendGate   sync.Mutex
	lastPacing time.Time
	inbound    chan inboundPrompt
	dropped    atomic.Uint64

	inboundDedup *boundedCache[inboundKey]
	sentBot      *boundedCache[sentKey]
	prefixes     []string
}

type ioReader interface{ Read([]byte) (int, error) }

func NewChannel(bc *config.Channel, cfg *config.MeshtasticSettings, b *bus.MessageBus) (*Channel, error) {
	resolved, indices, baseAddress, err := validateSettings(cfg)
	if err != nil {
		return nil, fmt.Errorf("meshtastic configuration: %w", err)
	}
	base := channels.NewBaseChannel(bc.Name(), &resolved, b, bc.AllowFrom,
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID))
	c := &Channel{
		BaseChannel: base, bc: bc, cfg: resolved, indices: indices,
		textChunkBytes: *resolved.TextChunkBytes,
		sendDelay:      time.Duration(*resolved.SendDelayMS) * time.Millisecond,
		handshakeIdle:  handshakeTimeout,
		rejectionGrace: localRejectGrace,
		random:         rand.Reader, now: time.Now,
		pending:      make(map[uint32]*acceptance),
		inbound:      make(chan inboundPrompt, inboundQueueSize),
		inboundDedup: newBoundedCache[inboundKey](1000, 5*time.Minute),
		sentBot:      newBoundedCache[sentKey](500, 60*time.Minute),
		queueChanged: make(chan struct{}), dutyChanged: make(chan struct{}),
	}
	c.state.changed = make(chan struct{})
	if resolved.Transport == "serial" {
		c.newTransport = func() transport { return newSerialTransport(resolved.SerialPort) }
	} else {
		c.newTransport = func() transport { return newHTTPTransport(baseAddress) }
	}
	for _, p := range bc.GroupTrigger.Prefixes {
		if p != "" {
			c.prefixes = append(c.prefixes, p)
		}
	}
	base.SetOwner(c)
	return c, nil
}

func validateSettings(in *config.MeshtasticSettings) (config.MeshtasticSettings, []uint32, string, error) {
	if in == nil {
		return config.MeshtasticSettings{}, nil, "", errors.New("settings are required")
	}
	cfg := *in
	if cfg.TextChunkBytes == nil {
		v := 200
		cfg.TextChunkBytes = &v
	}
	if *cfg.TextChunkBytes <= 0 {
		return cfg, nil, "", errors.New("text_chunk_bytes must be positive")
	}
	if cfg.SendDelayMS == nil {
		v := 2000
		cfg.SendDelayMS = &v
	}
	if *cfg.SendDelayMS < 2000 {
		return cfg, nil, "", errors.New("send_delay_ms must be at least 2000")
	}
	if cfg.ChannelIndices == nil {
		cfg.ChannelIndices = []int{0}
	}
	if len(cfg.ChannelIndices) == 0 {
		return cfg, nil, "", errors.New("channel_indices must not be empty")
	}
	seen := [8]bool{}
	indices := make([]uint32, 0, len(cfg.ChannelIndices))
	for _, i := range cfg.ChannelIndices {
		if i < 0 || i > 7 {
			return cfg, nil, "", fmt.Errorf("channel index %d is outside 0..7", i)
		}
		if seen[i] {
			return cfg, nil, "", fmt.Errorf("duplicate channel index %d", i)
		}
		seen[i] = true
		indices = append(indices, uint32(i))
	}
	switch cfg.Transport {
	case "serial":
		if strings.TrimSpace(cfg.SerialPort) == "" {
			return cfg, nil, "", errors.New("serial_port is required for serial transport")
		}
		if cfg.HTTPAddress != "" {
			return cfg, nil, "", errors.New("http_address is invalid for serial transport")
		}
		return cfg, indices, "", nil
	case "http":
		if cfg.SerialPort != "" {
			return cfg, nil, "", errors.New("serial_port is invalid for HTTP transport")
		}
		address, err := validateHTTPAddress(cfg.HTTPAddress)
		if err != nil {
			return cfg, nil, "", err
		}
		return cfg, indices, address, nil
	default:
		return cfg, nil, "", errors.New(`transport must be exactly "serial" or "http"`)
	}
}

func validateHTTPAddress(address string) (string, error) {
	if address == "" {
		return "", errors.New("http_address is required for HTTP transport")
	}
	if strings.Contains(address, "://") {
		if strings.HasPrefix(strings.ToLower(address), "https://") {
			return "", errors.New("HTTPS is unsupported in Meshtastic v1")
		}
		return "", errors.New("http_address must not contain a scheme")
	}
	u, err := url.Parse("http://" + address)
	if err != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Host != address {
		return "", errors.New("http_address must be a host or host:port without path, credentials, query, or fragment")
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("http_address has no host")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	if p, err := strconv.ParseUint(port, 10, 16); err != nil || p == 0 {
		return "", errors.New("http_address has an invalid port")
	}
	return net.JoinHostPort(host, port), nil
}

func (c *Channel) Start(ctx context.Context) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.stopped {
		return fmt.Errorf("meshtastic channel is one-shot: %w", channels.ErrNotRunning)
	}
	if c.started {
		if c.runCtx.Err() != nil {
			c.stopped = true
			return fmt.Errorf("meshtastic channel lifecycle ended: %w", channels.ErrNotRunning)
		}
		return nil
	}
	c.runCtx, c.cancel = context.WithCancel(ctx)
	c.started = true
	c.SetRunning(true)
	c.wg.Add(2)
	go c.lifecycle()
	go c.dispatcher()
	return nil
}

func (c *Channel) Stop(_ context.Context) error {
	c.lifecycleMu.Lock()
	if c.stopped {
		started := c.started
		c.lifecycleMu.Unlock()
		if started {
			c.wg.Wait()
		}
		return nil
	}
	c.stopped = true
	c.SetRunning(false)
	cancel := c.cancel
	started := c.started
	c.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		c.wg.Wait()
	}
	return nil
}

func (c *Channel) dispatcher() {
	defer c.wg.Done()
	for {
		select {
		case p := <-c.inbound:
			if c.runCtx.Err() != nil {
				return
			}
			if err := c.HandleInboundContext(c.runCtx, p.chatID, p.content, nil, p.ctx, p.sender); err != nil && c.runCtx.Err() == nil {
				logger.WarnCF("meshtastic", "inbound publish failed", map[string]any{"channel": c.Name(), "error": err.Error()})
			}
		case <-c.runCtx.Done():
			return
		}
	}
}

func (c *Channel) lifecycle() {
	defer c.wg.Done()
	defer func() {
		c.SetRunning(false)
		c.lifecycleMu.Lock()
		c.stopped = true
		c.lifecycleMu.Unlock()
	}()
	for c.runCtx.Err() == nil {
		configID, err := c.randomNonzero()
		if err != nil {
			logger.WarnCF("meshtastic", "config ID generation failed", map[string]any{"channel": c.Name(), "error": err.Error()})
			if waitContext(c.runCtx, reconnectDelay) != nil {
				return
			}
			continue
		}
		a := c.newAttempt(configID)
		err = c.runAttempt(a)
		c.teardownAttempt(a, err)
		if c.runCtx.Err() != nil {
			return
		}
		logger.WarnCF("meshtastic", "connection attempt ended", map[string]any{"channel": c.Name(), "error": errString(err)})
		if waitContext(c.runCtx, reconnectDelay) != nil {
			return
		}
	}
}

func (c *Channel) newAttempt(configID uint32) *attemptState {
	ctx, cancel := context.WithCancel(c.runCtx)
	return &attemptState{ctx: ctx, cancel: cancel, t: c.newTransport(), configID: configID, channels: make(map[uint32]mesh.Channel_Role)}
}

func (c *Channel) runAttempt(a *attemptState) error {
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); <-a.ctx.Done(); _ = a.t.Close() }()
	defer func() { a.cancel(); _ = a.t.Close(); <-watchDone }()
	c.setAttempt(a, false)
	if err := a.t.Open(a.ctx); err != nil {
		return err
	}
	if err := c.sendWantConfig(a); err != nil {
		return err
	}
	newHandshakeWatchdog := func() *idleWatchdog {
		return startIdleWatchdog(a.ctx, c.handshakeIdle, func() { _ = a.t.Close() })
	}
	handshakeWatchdog := newHandshakeWatchdog()
	defer func() {
		if handshakeWatchdog != nil {
			handshakeWatchdog.Stop()
		}
	}()

	var heartbeatDone chan struct{}
	if st, ok := a.t.(*serialTransport); ok {
		heartbeatDone = make(chan struct{})
		go c.heartbeatLoop(a, st, heartbeatDone)
		defer func() { a.cancel(); <-heartbeatDone }()
	}
	for {
		fr, err := a.t.ReceiveFromRadio(a.ctx)
		if err != nil {
			if handshakeWatchdog != nil && handshakeWatchdog.Expired() {
				return errors.New("Meshtastic configuration handshake timed out")
			}
			return err
		}
		if fr == nil {
			return errors.New("transport returned nil FromRadio")
		}
		if handshakeWatchdog != nil && !handshakeWatchdog.Reset(a.ctx) {
			if handshakeWatchdog.Expired() {
				return errors.New("Meshtastic configuration handshake timed out")
			}
			return a.ctx.Err()
		}
		if fr.GetRebooted() {
			c.invalidateAttempt(a, terminalAmbiguous, "radio rebooted")
			id, randErr := c.randomNonzero()
			if randErr != nil {
				return fmt.Errorf("post-reboot config ID: %w", randErr)
			}
			a.configID, a.gotMyInfo, a.ownNode, a.shortName = id, false, 0, ""
			a.channels = make(map[uint32]mesh.Channel_Role)
			if err := c.sendWantConfig(a); err != nil {
				return err
			}
			if handshakeWatchdog == nil {
				handshakeWatchdog = newHandshakeWatchdog()
			}
			continue
		}
		readyBefore := c.attemptReady(a)
		if err := c.processEnvelope(a, fr); err != nil {
			return err
		}
		if !readyBefore && c.attemptReady(a) {
			handshakeWatchdog.Stop()
			handshakeWatchdog = nil
		}
	}
}

func (c *Channel) sendWantConfig(a *attemptState) error {
	msg := &mesh.ToRadio{}
	msg.SetWantConfigId(a.configID)
	return a.t.SendToRadio(a.ctx, msg)
}

func (c *Channel) heartbeatLoop(a *attemptState, st *serialTransport, done chan<- struct{}) {
	defer close(done)
	t := time.NewTimer(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case now := <-t.C:
			idle := st.idleFor(now)
			if idle >= heartbeatInterval {
				m := &mesh.ToRadio{}
				m.SetHeartbeat(&mesh.Heartbeat{Nonce: 0})
				if err := st.SendToRadio(a.ctx, m); err != nil {
					a.cancel()
					return
				}
				t.Reset(heartbeatInterval)
			} else {
				t.Reset(heartbeatInterval - idle)
			}
		}
	}
}

func (c *Channel) setAttempt(a *attemptState, ready bool) {
	c.stateMu.Lock()
	c.state.attempt, c.state.ready = a, ready
	c.notifyStateLocked()
	c.stateMu.Unlock()
}

func (c *Channel) notifyStateLocked() { close(c.state.changed); c.state.changed = make(chan struct{}) }

func (c *Channel) attemptReady(a *attemptState) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.attempt == a && c.state.ready
}

func (c *Channel) publishReady(a *attemptState) {
	c.stateMu.Lock()
	if c.state.attempt == a {
		c.state.ready, c.state.ownNode, c.state.shortName = true, a.ownNode, a.shortName
		c.state.channels = make(map[uint32]mesh.Channel_Role, len(a.channels))
		for k, v := range a.channels {
			c.state.channels[k] = v
		}
		c.notifyStateLocked()
	}
	c.stateMu.Unlock()
	logger.DebugCF("meshtastic", "configuration handshake completed", map[string]any{
		"channel": c.Name(), "node": nodeID(a.ownNode), "configured_channels": len(a.channels),
	})
	for _, idx := range c.indices {
		if !usableRole(a.channels[idx]) {
			logger.WarnCF("meshtastic", "configured channel index is unusable", map[string]any{"channel": c.Name(), "index": idx})
		}
	}
}

func (c *Channel) invalidateAttempt(a *attemptState, term terminalKind, reason string) {
	c.stateMu.Lock()
	if c.state.attempt == a {
		c.state.ready, c.state.ownNode, c.state.shortName, c.state.channels = false, 0, "", nil
		c.notifyStateLocked()
	}
	c.stateMu.Unlock()
	c.failPending(a, term, reason)
}

func (c *Channel) teardownAttempt(a *attemptState, err error) {
	c.invalidateAttempt(a, terminalAmbiguous, errString(err))
	a.cancel()
	_ = a.t.Close()
}

func errString(err error) string {
	if err == nil {
		return "connection closed"
	}
	return err.Error()
}

func usableRole(r mesh.Channel_Role) bool {
	return r == mesh.Channel_PRIMARY || r == mesh.Channel_SECONDARY
}

func (c *Channel) randomNonzero() (uint32, error) {
	var b [4]byte
	for {
		if _, err := io.ReadFull(c.random, b[:]); err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint32(b[:])
		if v != 0 {
			return v, nil
		}
	}
}
