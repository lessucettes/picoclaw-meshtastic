// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func init() {
	channels.RegisterSafeFactory(config.ChannelMeshtastic,
		func(bc *config.Channel, cfg *config.MeshtasticSettings, b *bus.MessageBus) (channels.Channel, error) {
			return NewChannel(bc, cfg, b)
		})
}
