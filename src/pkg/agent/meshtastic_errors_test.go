// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestMeshtasticProcessingErrorsAreMarked(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()

	for _, channel := range []string{"mesh_home", "telegram"} {
		for _, err := range []error{
			errors.New("model unavailable"),
			errors.New("connection reset by peer"),
			context.DeadlineExceeded,
		} {
			if !al.maybePublishError(context.Background(), channel, "chat", "session", err) {
				t.Fatal("unexpected canceled processing")
			}
			select {
			case msg := <-msgBus.OutboundChan():
				if msg.Context.Raw["is_error"] != "true" || msg.Content != formatProcessingError(err) ||
					msg.Channel != channel || msg.ChatID != "chat" || msg.SessionKey != "session" ||
					msg.Context.Raw["outbound_kind"] != "final" {
					t.Fatalf("unexpected processing error: %+v", msg)
				}
			default:
				t.Fatal("missing processing error")
			}
		}
	}
	if al.maybePublishError(context.Background(), "mesh_home", "chat", "session", context.Canceled) {
		t.Fatal("cancellation should not publish an error")
	}
	al.PublishResponseIfNeeded(context.Background(), "mesh_home", "chat", "session", "Error: quoted user content")
	msg := <-msgBus.OutboundChan()
	if msg.Context.Raw["is_error"] == "true" {
		t.Fatal("ordinary text must not be classified as a runtime error")
	}
	select {
	case msg := <-msgBus.OutboundChan():
		t.Fatalf("unexpected extra outbound: %+v", msg)
	default:
	}
}

type diagnosticResultTool struct {
	echoTextTool
	result *tools.ToolResult
}

func (t *diagnosticResultTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return t.result
}

type diagnosticAsyncTool struct{ diagnosticResultTool }

func (t *diagnosticAsyncTool) ExecuteAsync(ctx context.Context, _ map[string]any, cb tools.AsyncCallback) *tools.ToolResult {
	// Invoke deterministically to exercise the actual pipeline callback without a worker race.
	cb(ctx, t.result)
	return tools.AsyncResult("scheduled")
}

type diagnosticResultHook struct{ result *tools.ToolResult }

func (h *diagnosticResultHook) BeforeTool(_ context.Context, req *ToolCallHookRequest) (*ToolCallHookRequest, HookDecision, error) {
	next := req.Clone()
	next.HookResult = h.result
	return next, HookDecision{Action: HookActionRespond}, nil
}

func (h *diagnosticResultHook) AfterTool(_ context.Context, resp *ToolResultHookResponse) (*ToolResultHookResponse, HookDecision, error) {
	return resp, HookDecision{Action: HookActionContinue}, nil
}

func TestMeshtasticToolErrorMetadata(t *testing.T) {
	for _, mode := range []string{"sync", "async", "hook"} {
		for _, isError := range []bool{false, true} {
			name := mode + "/success"
			if isError {
				name = mode + "/error"
			}
			t.Run(name, func(t *testing.T) {
				provider := &multiToolProvider{
					toolCalls:    []providers.ToolCall{{ID: "call-1", Name: "echo_text", Arguments: map[string]any{}}},
					finalContent: "final response",
				}
				al, agent, cleanup := newHookTestLoop(t, provider)
				defer cleanup()
				result := &tools.ToolResult{ForLLM: "diagnostic", ForUser: "tool notification", IsError: isError}
				tool := diagnosticResultTool{result: result}
				if mode == "async" {
					al.RegisterTool(&diagnosticAsyncTool{diagnosticResultTool: tool})
				} else {
					al.RegisterTool(&tool)
				}
				if mode == "hook" {
					if err := al.MountHook(NamedHook("diagnostic", &diagnosticResultHook{result: result})); err != nil {
						t.Fatal(err)
					}
				}
				inbound := &bus.InboundContext{
					Channel: "mesh_home", ChatID: "channel:0", MessageID: "77",
					Raw: map[string]string{"meshtastic_channel_index": "0"},
				}
				_, err := al.runAgentLoop(context.Background(), agent, processOptions{
					Dispatch:     DispatchRequest{SessionKey: "session", UserMessage: "use tool", InboundContext: inbound},
					SendResponse: true, SuppressToolFeedback: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				msgBus := al.bus.(*bus.MessageBus)
				var toolMessages, finalMessages int
				for len(msgBus.OutboundChan()) > 0 {
					msg := <-msgBus.OutboundChan()
					switch msg.Content {
					case "tool notification":
						toolMessages++
						if (msg.Context.Raw["is_error"] == "true") != isError {
							t.Fatalf("wrong tool error metadata: %+v", msg)
						}
						if mode != "hook" && (msg.Context.MessageID != "77" || msg.SessionKey != "session") {
							t.Fatalf("lost tool response context: %+v", msg)
						}
					case "final response":
						finalMessages++
						if msg.Context.Raw["is_error"] == "true" {
							t.Fatal("tool error marker leaked into final model response")
						}
					default:
						t.Fatalf("unexpected outbound: %+v", msg)
					}
				}
				if toolMessages != 1 || finalMessages != 1 {
					t.Fatalf("tool messages=%d final messages=%d, want one each", toolMessages, finalMessages)
				}
				if inbound.Raw["is_error"] != "" || result.IsError != isError || result.ForLLM != "diagnostic" {
					t.Fatal("outbound classification modified inbound context or tool result")
				}
			})
		}
	}
}

type diagnosticContextProvider struct{ mockProvider }

func (*diagnosticContextProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	return nil, errors.New("context_length_exceeded")
}

func TestMeshtasticContextRetryNoticeIsMarked(t *testing.T) {
	al, agent, cleanup := newHookTestLoop(t, &diagnosticContextProvider{})
	defer cleanup()
	_, err := al.runAgentLoop(context.Background(), agent, processOptions{
		Dispatch: DispatchRequest{
			SessionKey: "session", UserMessage: "hello",
			InboundContext: &bus.InboundContext{Channel: "mesh_home", ChatID: "channel:0"},
		},
		SuppressToolFeedback: true,
	})
	if err == nil {
		t.Fatal("expected provider failure")
	}
	select {
	case msg := <-al.bus.(*bus.MessageBus).OutboundChan():
		if !strings.HasPrefix(msg.Content, "Context window exceeded.") || msg.Context.Raw["is_error"] != "true" {
			t.Fatalf("unexpected retry notice: %+v", msg)
		}
	default:
		t.Fatal("missing context retry notice")
	}
}
