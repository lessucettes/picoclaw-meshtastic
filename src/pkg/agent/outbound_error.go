// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// withOutboundError labels runtime diagnostics without inspecting user-visible text.
// Channels may suppress these messages without changing tool results or retries.
func withOutboundError(msg bus.OutboundMessage, isError bool) bus.OutboundMessage {
	if isError {
		if msg.Context.Raw == nil {
			msg.Context.Raw = make(map[string]string, 1)
		}
		msg.Context.Raw["is_error"] = "true"
	}
	return msg
}

func outboundMessageForToolResult(ts *turnState, result *tools.ToolResult) bus.OutboundMessage {
	return withOutboundError(outboundMessageForTurn(ts, result.ForUser), result.IsError)
}

// channelSuppressesErrors also gates synthesized final diagnostics before the
// legacy string-returning agent boundary loses their provenance.
func (al *AgentLoop) channelSuppressesErrors(channel string) bool {
	if al.channelManager == nil {
		return false
	}
	ch, ok := al.channelManager.GetChannel(channel)
	if !ok {
		return false
	}
	policy, ok := ch.(interface{ SuppressErrors() bool })
	return ok && policy.SuppressErrors()
}
