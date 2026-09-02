# Meshtastic Channel

PicoClaw can use a locally attached Meshtastic node through the official Serial Stream API or the node's HTTP Phone API. The integration reads node/channel state and sends text packets; it does not change radio or channel configuration.

## Serial setup

```json
{
  "channel_list": {
    "mesh_home": {
      "enabled": true,
      "type": "meshtastic",
      "allow_from": ["!a1b2c3d4"],
      "group_trigger": {
        "mention_only": false,
        "prefixes": ["pico:"]
      },
      "settings": {
        "transport": "serial",
        "serial_port": "/dev/ttyACM0",
        "channel_indices": [0]
      }
    }
  }
}
```

Serial is always opened at 115200 baud. The PicoClaw process must have permission to access the device.

## HTTP setup

```json
{
  "channel_list": {
    "mesh_wifi": {
      "enabled": true,
      "type": "meshtastic",
      "allow_from": ["!a1b2c3d4"],
      "group_trigger": {
        "mention_only": true
      },
      "settings": {
        "transport": "http",
        "http_address": "meshtastic.local",
        "channel_indices": [0, 1],
        "commands": ["help", "nodes", "stats"],
        "text_chunk_bytes": 200,
        "send_delay_ms": 2000
      }
    }
  }
}
```

`http_address` is a host or host:port without a scheme or path. Port 80 is used when omitted. HTTPS is not supported in v1.

Start the configured gateway with:

```bash
picoclaw gateway
```

## Settings

| Setting | Required/default | Meaning |
|---|---|---|
| `transport` | required | Exactly `serial` or `http`. |
| `serial_port` | required for Serial | OS serial device path; invalid with HTTP. |
| `http_address` | required for HTTP | Host or host:port; invalid with Serial. |
| `channel_indices` | `[0]` | Unique active Primary/Secondary indexes in `0..7`; controls public channels and the proactive-DM fallback. |
| `commands` | omitted | PicoClaw command names allowed from this channel. Omit to allow all commands, use `[]` to block every command, or list bare names such as `["help", "nodes", "stats"]` to allow only those names. |
| `text_chunk_bytes` | `200` | Positive soft UTF-8 byte target, including visible chunk numbering. Exact protobuf/LoRa limits are always checked separately. |
| `send_delay_ms` | `2000` (minimum `2000`) | Minimum interval between physical text submissions for this channel instance. |

The environment variables `PICOCLAW_CHANNELS_MESHTASTIC_TRANSPORT`, `..._SERIAL_PORT`, `..._HTTP_ADDRESS`, `..._CHANNEL_INDICES`, `..._TEXT_CHUNK_BYTES`, and `..._SEND_DELAY_MS` are type-wide overrides. Use per-instance JSON settings when named Meshtastic instances need different values.

## Conversations and triggers

Direct messages always reach the agent when `allow_from` permits their node ID. Replies are returned to the same node and preserve the device channel index and native Meshtastic `reply_id`.

Public channel messages share `channel:N` sessions. Trigger precedence is:

1. a native reply to a recent bot packet;
2. a literal `@!NodeID` or `@ShortName` mention of this node;
3. a configured prefix;
4. the permissive group fallback when `mention_only` is false and no prefixes are configured.

Unrelated public chatter is ignored. Prefixes and the selected bot mention are removed from the prompt. The channel first looks for the canonical own-node ID form, such as `@!698508e0`, and then falls back to the current Meshtastic short name, such as `@GubB`. Matching is case-insensitive and requires token boundaries. If both forms occur, the Node ID mention has priority regardless of their textual order.

## Command filtering

The optional `commands` setting filters PicoClaw commands for both direct and public-channel messages. `/command` and `!command` forms are recognized. Matching is case-insensitive and uses only the first, top-level command name; arguments do not affect the decision. The filter runs after a configured group prefix or bot mention is removed and whitespace is normalized, so a message such as `@Bot /stats now` is checked as the `stats` command.

The setting is independent of `allow_from` and does not grant privileges to any NodeID. A blocked command is consumed silently before it reaches PicoClaw or the LLM. Messages without a leading command prefix are unchanged. Command names are not checked against a hard-coded built-in list, so the policy also applies to commands added by PicoClaw in the future.

## Security

Use canonical node IDs such as `!a1b2c3d4`, `meshtastic:!a1b2c3d4`, or `*` in `allow_from`. This is a routing/access filter, not sender authentication: legacy PSK and public-channel sender IDs can be spoofed. Successfully PKI-decrypted packets are exposed separately as `meshtastic_pki_encrypted=true`, but PicoClaw does not manage Meshtastic keys or a foreign-node key cache.

## Dependencies

The channel uses the maintained pure-Go `go.bug.st/serial` v1.8.0 implementation and generated Meshtastic types from Buf Schema Registry with `google.golang.org/protobuf` v1.36.12. The requested BSR build `v1.36.12-20260819193617-20c48ef3f04b.1` contains duplicate `AS3935Config` declarations and does not compile; this implementation therefore pins the last verified compatible pre-regression build, `v1.36.12-20260811120449-6b706c172b5b.1`. Its fields used here were checked against official Meshtastic schemas and target firmware behavior. The obsolete `github.com/meshtastic/go` client and cgo are not used.

## Known v1 limitations

- Host-side MQTT, HTTPS, media, reactions/tapbacks, telemetry, traceroute, and device configuration are outside v1 scope. Packets that reached the attached radio through device MQTT remain normal eligible input.
- Meshtastic's proto3 `rx_snr` field has no presence bit in the available generated schemas. PicoClaw therefore always publishes `meshtastic_rx_snr`, including `0`; optional `rx_rssi` is omitted when absent.
- Deduplication and native-reply correlation caches are bounded in memory and are cleared by process restart.
- For unicast channel index 0, target firmware may substitute a remembered NodeDB channel. PicoClaw still submits index 0 and does not force PKI or mutate device state. Broadcast `channel:0` is unaffected.
- Accepted delivery means the attached firmware reported local queue success and no correlated local rejection arrived during a four-second window. A valid local echo, when a transport supplies one, completes that boundary immediately. Current physical firmware does not loop host-originated packets back to Phone API clients; Portduino SimRadio does. This boundary is not an end-to-end mesh acknowledgement.
- The generated Meshtastic schema module is GPL-3.0-only. Builds containing the channel are therefore GPL-3.0-only; see the standalone project's licensing documentation.
