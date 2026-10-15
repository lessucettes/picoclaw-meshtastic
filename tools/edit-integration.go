// SPDX-License-Identifier: GPL-3.0-only

// edit-integration applies the canonical, anchor-checked PicoClaw integration
// edits. The reviewable patch is generated from these edits; it is not a
// second hand-maintained source of truth.
package main

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
)

type edit struct {
	path   string
	old    string
	new    string
	goFile bool
}

func main() {
	if len(os.Args) != 2 {
		fatalf("usage: go run ./tools/edit-integration.go PICOCLAW_ROOT")
	}
	root, err := filepath.Abs(os.Args[1])
	if err != nil {
		fatalf("resolve PicoClaw root: %v", err)
	}

	edits := []edit{
		{
			path: "pkg/agent/agent.go",
			old:  `	DefaultResponse         string                 // Response when LLM returns empty`,
			new: `	DefaultResponse         string                 // Response when LLM returns empty
	DefaultResponseIsError  bool                   // The configured fallback is a runtime diagnostic`,
			goFile: true,
		},
		{
			path: "pkg/agent/agent_message.go",
			old:  `		DefaultResponse:      defaultResponse,`,
			new: `		DefaultResponse:      defaultResponse,
		DefaultResponseIsError: true,`,
			goFile: true,
		},
		{
			path: "pkg/agent/agent_message.go",
			old:  `		DefaultResponse:         defaultResponse,`,
			new: `		DefaultResponse:         defaultResponse,
		DefaultResponseIsError: true,`,
			goFile: true,
		},
		{
			path: "pkg/agent/steering.go",
			old:  `		DefaultResponse:         defaultResponse,`,
			new: `		DefaultResponse:         defaultResponse,
		DefaultResponseIsError: true,`,
			goFile: true,
		},
		{
			path: "pkg/agent/turn_state.go",
			old: `type turnResult struct {
	finalContent string`,
			new: `type turnResult struct {
	runtimeDiagnostic bool
	finalContent string`,
			goFile: true,
		},
		{
			path: "pkg/agent/turn_state.go",
			old: `	// Turn output
	finalContent string`,
			new: `	// Turn output
	runtimeDiagnostic bool
	finalContent string`,
			goFile: true,
		},
		{
			path: "pkg/agent/turn_coord.go",
			old: `			if finalContent == "" {
				finalContent = ts.opts.DefaultResponse
			}`,
			new: `			if finalContent == "" {
				finalContent = ts.opts.DefaultResponse
				exec.runtimeDiagnostic = ts.opts.DefaultResponseIsError
			}`,
			goFile: true,
		},
		{
			path: "pkg/agent/turn_coord.go",
			old: `			finalContent = toolLimitResponse
		} else {
			finalContent = ts.opts.DefaultResponse
		}`,
			new: `			finalContent = toolLimitResponse
			exec.runtimeDiagnostic = true
		} else {
			finalContent = ts.opts.DefaultResponse
			exec.runtimeDiagnostic = ts.opts.DefaultResponseIsError
		}`,
			goFile: true,
		},
		{
			path: "pkg/agent/pipeline_finalize.go",
			old:  `		!ts.opts.SendResponse && ts.opts.AllowInterimPicoPublish && finalContent != "" {`,
			new: `		!ts.opts.SendResponse && ts.opts.AllowInterimPicoPublish && finalContent != "" &&
		!(exec.runtimeDiagnostic && al.channelSuppressesErrors(ts.channel)) {`,
			goFile: true,
		},
		{
			path: "pkg/agent/pipeline_finalize.go",
			old: `	return turnResult{
		finalContent: finalContent,
		modelName:`,
			new: `	return turnResult{
		runtimeDiagnostic: exec.runtimeDiagnostic,
		finalContent: finalContent,
		modelName:`,
			goFile: true,
		},
		{
			path: "pkg/agent/agent.go",
			old:  `	if opts.SendResponse && result.finalContent != "" {`,
			new: `	suppressDiagnostic := result.runtimeDiagnostic && al.channelSuppressesErrors(opts.Dispatch.Channel())
	if opts.SendResponse && result.finalContent != "" && !suppressDiagnostic {`,
			goFile: true,
		},
		{
			path: "pkg/agent/agent.go",
			old:  `	return result.finalContent, nil`,
			new: `	if suppressDiagnostic {
		return "", nil
	}
	return result.finalContent, nil`,
			goFile: true,
		},
		{
			path: "README.md",
			old:  "| **MQTT** | Easy (broker + agent_id) | MQTT pub/sub | [Guide](docs/channels/mqtt/README.md) |\n| **MaixCam**",
			new:  "| **MQTT** | Easy (broker + agent_id) | MQTT pub/sub | [Guide](docs/channels/mqtt/README.md) |\n| **Meshtastic** | Easy (local radio) | Serial / HTTP Phone API | [Guide](docs/channels/meshtastic/README.md) |\n| **MaixCam**",
		},
		{
			path: "docs/guides/chat-apps.md",
			old:  "OneBot, MQTT, MaixCam, or Pico (native protocol)",
			new:  "OneBot, MQTT, Meshtastic, MaixCam, or Pico (native protocol)",
		},
		{
			path: "docs/guides/chat-apps.md",
			old:  "| **MQTT**             | ⭐ Easy            | Any MQTT client via broker pub/sub                    | [Docs](../channels/mqtt/README.md)                                                                              |\n| **MaixCam**",
			new:  "| **MQTT**             | ⭐ Easy            | Any MQTT client via broker pub/sub                    | [Docs](../channels/mqtt/README.md)                                                                              |\n| **Meshtastic**       | ⭐ Easy            | Local mesh radio over Serial or HTTP Phone API        | [Docs](../channels/meshtastic/README.md)                                                                       |\n| **MaixCam**",
		},
		{
			path: "pkg/agent/agent_outbound.go",
			old:  `	al.PublishResponseIfNeeded(ctx, channel, chatID, sessionKey, formatProcessingError(err))`,
			new: `	logger.ErrorCF("agent", "Message processing failed", map[string]any{
		"channel": channel, "chat_id": chatID, "error": err.Error(),
	})
	al.publishResponseForInboundIfNeeded(ctx, channel, chatID, sessionKey, formatProcessingError(err),
		&bus.InboundContext{Raw: map[string]string{"is_error": "true"}})`,
			goFile: true,
		},
		{
			path: "pkg/agent/pipeline_execute.go",
			old: `al.bus.PublishOutbound(ctx, bus.OutboundMessage{
							Context: bus.InboundContext{`,
			new: `al.bus.PublishOutbound(ctx, withOutboundError(bus.OutboundMessage{
							Context: bus.InboundContext{`,
			goFile: true,
		},
		{
			path: "pkg/agent/pipeline_execute.go",
			old: `							Content: hookResult.ForUser,
						})`,
			new: `							Content: hookResult.ForUser,
						}, hookResult.IsError))`,
			goFile: true,
		},
		{
			path:   "pkg/agent/pipeline_execute.go",
			old:    `outboundMessageForTurn(ts, result.ForUser)`,
			new:    `outboundMessageForToolResult(ts, result)`,
			goFile: true,
		},
		{
			path:   "pkg/agent/pipeline_execute.go",
			old:    `outboundMessageForTurn(ts, toolResult.ForUser)`,
			new:    `outboundMessageForToolResult(ts, toolResult)`,
			goFile: true,
		},
		{
			path: "pkg/agent/pipeline_llm.go",
			old: `				al.bus.PublishOutbound(ctx, outboundMessageForTurn(
					ts,
					"Context window exceeded. Compressing history and retrying...",
				))`,
			new: `				al.bus.PublishOutbound(ctx, withOutboundError(outboundMessageForTurn(
					ts,
					"Context window exceeded. Compressing history and retrying...",
				), true))`,
			goFile: true,
		},
		{
			path: "pkg/agent/agent_outbound.go",
			old: `func (al *AgentLoop) PublishResponseIfNeeded(ctx context.Context, channel, chatID, sessionKey, response string) {
	if response == "" {`,
			new: `func (al *AgentLoop) PublishResponseIfNeeded(ctx context.Context, channel, chatID, sessionKey, response string) {
	al.publishResponseIfNeeded(ctx, channel, chatID, sessionKey, response, nil)
}

func (al *AgentLoop) publishResponseForInboundIfNeeded(
	ctx context.Context,
	channel, chatID, sessionKey, response string,
	inbound *bus.InboundContext,
) {
	al.publishResponseIfNeeded(ctx, channel, chatID, sessionKey, response, inbound)
}

func (al *AgentLoop) publishResponseIfNeeded(
	ctx context.Context,
	channel, chatID, sessionKey, response string,
	inbound *bus.InboundContext,
) {
	if response == "" {`,
			goFile: true,
		},
		{
			path: "pkg/agent/agent_outbound.go",
			old: `	msg := bus.OutboundMessage{
		Context:    bus.NewOutboundContext(channel, chatID, ""),
		SessionKey: sessionKey,
		Content:    response,
	}`,
			new: `	outboundCtx := bus.NewOutboundContext(channel, chatID, "")
	if inbound != nil {
		outboundCtx = outboundContextFromInbound(inbound, channel, chatID, "")
	}
	msg := bus.OutboundMessage{
		Context:    outboundCtx,
		SessionKey: sessionKey,
		Content:    response,
	}`,
			goFile: true,
		},
		{
			path: "pkg/agent/agent_steering.go",
			old: `	if finalResponse != "" {
		al.PublishResponseIfNeeded(ctx, target.Channel, target.ChatID, target.SessionKey, finalResponse)
	}`,
			new: `	if finalResponse != "" {
		al.publishResponseForInboundIfNeeded(
			ctx,
			target.Channel,
			target.ChatID,
			target.SessionKey,
			finalResponse,
			&initialMsg.Context,
		)
	}`,
			goFile: true,
		},
		{
			path: "pkg/channels/manager.go",
			old: `	"irc":      2,
}`,
			new: `	"irc":        2,
	"meshtastic": 0.5,
}`,
			goFile: true,
		},
		{
			path: "pkg/config/config_channel.go",
			old: `	ChannelMQTT           = "mqtt"
	ChannelSlackWebHook`,
			new: `	ChannelMQTT           = "mqtt"
	ChannelMeshtastic     = "meshtastic"
	ChannelSlackWebHook`,
			goFile: true,
		},
		{
			path: "pkg/config/config_channel.go",
			old:  `// singletonRegistry stores which channel types are singletons (only allow one instance).`,
			new: "// MeshtasticSettings configures a direct Serial or HTTP Phone API connection.\n" +
				"type MeshtasticSettings struct {\n" +
				"\tTransport      string `json:\"transport\"                 yaml:\"-\" env:\"PICOCLAW_CHANNELS_MESHTASTIC_TRANSPORT\"`\n" +
				"\tSerialPort     string `json:\"serial_port,omitempty\"     yaml:\"-\" env:\"PICOCLAW_CHANNELS_MESHTASTIC_SERIAL_PORT\"`\n" +
				"\tHTTPAddress    string `json:\"http_address,omitempty\"    yaml:\"-\" env:\"PICOCLAW_CHANNELS_MESHTASTIC_HTTP_ADDRESS\"`\n" +
				"\tChannelIndices []int  `json:\"channel_indices,omitempty\" yaml:\"-\" env:\"PICOCLAW_CHANNELS_MESHTASTIC_CHANNEL_INDICES\"`\n" +
				"\tTextChunkBytes *int   `json:\"text_chunk_bytes,omitempty\" yaml:\"-\" env:\"PICOCLAW_CHANNELS_MESHTASTIC_TEXT_CHUNK_BYTES\"`\n" +
				"	SendDelayMS    *int   `json:\"send_delay_ms,omitempty\"    yaml:\"-\" env:\"PICOCLAW_CHANNELS_MESHTASTIC_SEND_DELAY_MS\"`\n" +
				"	SuppressErrors bool   `json:\"suppress_errors,omitempty\"  yaml:\"-\" env:\"PICOCLAW_CHANNELS_MESHTASTIC_SUPPRESS_ERRORS\"`\n" +
				"\t// Commands is a pointer so omitted (allow all) remains distinct from [] (block all).\n" +
				"\tCommands *[]string `json:\"commands,omitempty\"        yaml:\"-\"`\n" +
				"}\n\n" +
				"// singletonRegistry stores which channel types are singletons (only allow one instance).",
			goFile: true,
		},
		{
			path: "pkg/config/config_channel.go",
			old: `	ChannelMQTT:           (MQTTSettings{}),
	ChannelSlackWebHook:`,
			new: `	ChannelMQTT:           (MQTTSettings{}),
	ChannelMeshtastic:     (MeshtasticSettings{}),
	ChannelSlackWebHook:`,
			goFile: true,
		},
		{
			path: "pkg/gateway/gateway.go",
			old: `	_ "github.com/sipeed/picoclaw/pkg/channels/maixcam"
	_ "github.com/sipeed/picoclaw/pkg/channels/mqtt"`,
			new: `	_ "github.com/sipeed/picoclaw/pkg/channels/maixcam"
	_ "github.com/sipeed/picoclaw/pkg/channels/meshtastic"
	_ "github.com/sipeed/picoclaw/pkg/channels/mqtt"`,
			goFile: true,
		},
	}

	formatted := make(map[string]bool)
	for _, e := range edits {
		path := filepath.Join(root, filepath.FromSlash(e.path))
		data, err := os.ReadFile(path)
		if err != nil {
			fatalf("read %s: %v", e.path, err)
		}
		if n := strings.Count(string(data), e.old); n != 1 {
			fatalf("%s: integration anchor matched %d times, want exactly 1", e.path, n)
		}
		out := strings.Replace(string(data), e.old, e.new, 1)
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			fatalf("write %s: %v", e.path, err)
		}
		if e.goFile {
			formatted[e.path] = true
		}
	}
	for rel := range formatted {
		path := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			fatalf("read %s for formatting: %v", rel, err)
		}
		out, err := format.Source(data)
		if err != nil {
			fatalf("format %s: %v", rel, err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			fatalf("write formatted %s: %v", rel, err)
		}
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "edit-integration: "+format+"\n", args...)
	os.Exit(1)
}
