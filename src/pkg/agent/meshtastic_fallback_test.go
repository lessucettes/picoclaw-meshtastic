// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type diagnosticPolicyChannel struct {
	fakeChannel
	suppress bool
}

func (c *diagnosticPolicyChannel) SuppressErrors() bool { return c.suppress }

func setDiagnosticPolicy(t *testing.T, al *AgentLoop, suppress bool) {
	t.Helper()
	manager, err := channels.NewManager(&config.Config{}, al.bus.(*bus.MessageBus), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Do not start workers: outbound assertions must observe the actual bus.
	manager.RegisterChannel("mesh_home", &diagnosticPolicyChannel{suppress: suppress})
	manager.RegisterChannel("ordinary", &fakeChannel{})
	al.channelManager = manager
}

func TestMeshtasticSynthesizedDiagnostics(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		for _, send := range []bool{false, true} {
			for _, mode := range []string{"empty", "limit", "model-quotation", "custom-fallback"} {
				name := mode
				if suppress {
					name += "/suppressed"
				}
				if send {
					name += "/publish"
				}
				t.Run(name, func(t *testing.T) {
					provider := &multiToolProvider{}
					fallback := defaultResponse
					isDiagnostic := true
					if mode == "limit" {
						provider.toolCalls = []providers.ToolCall{{ID: "call-1", Name: "echo_text", Arguments: map[string]any{}}}
						fallback = toolLimitResponse
					} else if mode == "model-quotation" {
						provider.finalContent = defaultResponse
						isDiagnostic = false
					} else if mode == "custom-fallback" {
						fallback = "Background task completed."
						isDiagnostic = false
					}
					al, agent, cleanup := newHookTestLoop(t, provider)
					defer cleanup()
					setDiagnosticPolicy(t, al, suppress)
					agent.MaxIterations = 1
					al.RegisterTool(&diagnosticResultTool{result: tools.SilentResult("diagnostic")})
					response, err := al.runAgentLoop(context.Background(), agent, processOptions{
						Dispatch: DispatchRequest{SessionKey: "session", UserMessage: "hello",
							InboundContext: &bus.InboundContext{Channel: "mesh_home", ChatID: "channel:0"}},
						DefaultResponse: fallback, DefaultResponseIsError: mode != "custom-fallback",
						SendResponse: send, SuppressToolFeedback: true,
					})
					if err != nil {
						t.Fatal(err)
					}
					want := fallback
					if suppress && isDiagnostic {
						want = ""
					}
					if response != want {
						t.Fatalf("response=%q want=%q", response, want)
					}
					msgBus := al.bus.(*bus.MessageBus)
					wantOutbound := 0
					if send && want != "" {
						wantOutbound = 1
					}
					if len(msgBus.OutboundChan()) != wantOutbound {
						t.Fatalf("outbound=%d want=%d", len(msgBus.OutboundChan()), wantOutbound)
					}
					history := agent.Sessions.GetHistory("session")
					if len(history) == 0 || history[len(history)-1].Content != fallback {
						t.Fatalf("local history did not preserve synthesized text: %+v", history)
					}
				})
			}
		}
	}
}

func TestMeshtasticStreamingFallbackDiagnostic(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		al, agent, cleanup := newHookTestLoop(t, &mockProvider{})
		setDiagnosticPolicy(t, al, suppress)
		opts := processOptions{
			Dispatch: DispatchRequest{SessionKey: "session",
				InboundContext: &bus.InboundContext{Channel: "mesh_home", ChatID: "channel:0"}},
			AllowInterimPicoPublish: true, NoHistory: true,
		}
		ts := newTurnState(agent, opts, turnEventScope{})
		exec := &turnExecution{streamingFallback: true, runtimeDiagnostic: true}
		result, err := NewPipeline(al).Finalize(context.Background(), context.Background(), ts, exec, TurnEndStatusCompleted, defaultResponse)
		if err != nil || !result.runtimeDiagnostic || result.finalContent != defaultResponse {
			t.Fatalf("finalization changed diagnostic result: %+v err=%v", result, err)
		}
		want := 1
		if suppress {
			want = 0
		}
		if got := len(al.bus.(*bus.MessageBus).OutboundChan()); got != want {
			t.Fatalf("streaming fallback outbound=%d want=%d", got, want)
		}
		cleanup()
	}
}

func TestMeshtasticDiagnosticPolicyIsOptional(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	if al.channelSuppressesErrors("mesh_home") {
		t.Fatal("nil manager must preserve normal delivery")
	}
	setDiagnosticPolicy(t, al, true)
	if al.channelSuppressesErrors("missing") || al.channelSuppressesErrors("ordinary") || !al.channelSuppressesErrors("mesh_home") {
		t.Fatal("diagnostic policy did not respect channel capability")
	}
}
