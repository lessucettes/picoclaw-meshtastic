// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

type httpTransport struct {
	baseURL string

	mu      sync.Mutex
	client  *http.Client
	openCtx context.Context
	cancel  context.CancelFunc
	closed  bool
	writeMu sync.Mutex
	readMu  sync.Mutex
}

func newHTTPTransport(address string) *httpTransport {
	return &httpTransport{baseURL: "http://" + address}
}

func (t *httpTransport) Open(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		cancel()
		return context.Canceled
	}
	t.cancel = cancel
	t.openCtx = ctx
	t.client = &http.Client{
		Transport:     tr,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	_ = ctx
	return nil
}

func (t *httpTransport) getClient() (*http.Client, context.Context, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.client == nil {
		return nil, nil, errors.New("HTTP transport is closed")
	}
	return t.client, t.openCtx, nil
}

func (t *httpTransport) SendToRadio(ctx context.Context, msg *mesh.ToRadio) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return sendErr(fmt.Errorf("marshal ToRadio: %w", err), false)
	}
	if len(payload) > maxEnvelopeBytes {
		return sendErr(envelopeTooLarge("ToRadio", len(payload)), false)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	client, openCtx, err := t.getClient()
	if err != nil {
		return sendErr(err, false)
	}
	opCtx, opCancel := context.WithCancel(ctx)
	stopCloseCancel := context.AfterFunc(openCtx, opCancel)
	defer func() {
		stopCloseCancel()
		opCancel()
	}()
	mayHaveWritten := false
	trace := &httptrace.ClientTrace{
		WroteHeaders: func() { mayHaveWritten = true },
		WroteRequest: func(httptrace.WroteRequestInfo) { mayHaveWritten = true },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(opCtx, trace), http.MethodPut, t.baseURL+"/api/v1/toradio", bytes.NewReader(payload))
	if err != nil {
		return sendErr(err, false)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := client.Do(req)
	if err != nil {
		return sendErr(err, mayHaveWritten)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return sendErr(fmt.Errorf("Meshtastic HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), true)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEnvelopeBytes+1))
	if err != nil {
		return sendErr(err, true)
	}
	if len(body) > maxEnvelopeBytes {
		return sendErr(envelopeTooLarge("PUT response", len(body)), true)
	}
	return nil
}

func (t *httpTransport) ReceiveFromRadio(ctx context.Context) (*mesh.FromRadio, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()
	for {
		client, openCtx, err := t.getClient()
		if err != nil {
			return nil, err
		}
		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		stopCloseCancel := context.AfterFunc(openCtx, cancel)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t.baseURL+"/api/v1/fromradio?all=false", nil)
		if err != nil {
			stopCloseCancel()
			cancel()
			return nil, err
		}
		req.Header.Set("Accept", "application/x-protobuf")
		resp, err := client.Do(req)
		if err != nil {
			stopCloseCancel()
			cancel()
			return nil, err
		}
		body, readErr := t.readResponse(resp)
		stopCloseCancel()
		cancel()
		if readErr != nil {
			return nil, readErr
		}
		if len(body) == 0 {
			if err := waitContext(ctx, httpPollInterval); err != nil {
				return nil, err
			}
			continue
		}
		var out mesh.FromRadio
		if err := proto.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode FromRadio HTTP response: %w", err)
		}
		return &out, nil
	}
}

func (t *httpTransport) readResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("Meshtastic HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEnvelopeBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxEnvelopeBytes {
		return nil, envelopeTooLarge("FromRadio", len(body))
	}
	return body, nil
}

func (t *httpTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cancel, client := t.cancel, t.client
	t.cancel, t.client, t.openCtx = nil, nil, nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if client != nil {
		if tr, ok := client.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	}
	return nil
}
