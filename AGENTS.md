# AGENTS.md

This repository carries the native Meshtastic channel developed and debugged
against PicoClaw `bbf6893ca7afad27f1d00a0f5a45982a549c6ed6`. Treat the code,
not old client behavior or this prose, as the final authority. Before changing
protocol behavior, re-check the pinned firmware and schemas listed below.

## Working model

The project is not independently buildable as a Go module and must not become a
PicoClaw fork. Canonical installable files live under `src/`. Existing
PicoClaw files are changed by `tools/edit-integration.go`; the patch under
`integration/` is generated output.

For every change:

1. edit `src/` and, only if an existing PicoClaw file must change, the
   anchor-checked transformer;
2. run `./scripts/generate-patch.sh`;
3. inspect the patch for scope and `git diff --check`;
4. run `./scripts/verify.sh`, which tests a fresh baseline checkout;
5. run hardware/simulator checks only when transport or firmware-facing
   behavior changed.

Never hand-edit the generated patch. Do not copy a whole modified PicoClaw file
into this repository.

## Protocol references actually used

- PicoClaw: `bbf6893ca7afad27f1d00a0f5a45982a549c6ed6`.
- Meshtastic firmware: `6d41e279f1f51bd59f687b9d441c1bf47b1594fc`.
- Meshtastic schemas inspected at
  `ef0ae579e9de937f7fae25f9fdc5dbfe24a2fe2b`; generated BSR Go module is
  pinned in `integration/baseline.env`.
- Python client `0539a9600fc6c15eda398494ffb0309322cfb089`
  informed Serial framing/heartbeat/handshake.
- Android client `a276cda62c5e80e7eb044503b71096b8997fa005`
  informed reply and hops-away semantics.
- `@meshtastic/core` 2.6.7 and `@meshtastic/transport-http` 0.2.5 were
  interoperability references, not architecture templates.
- MeshClaw was consulted only for useful chat behavior. Do not import its
  OpenClaw/TypeScript account, runtime, or MQTT architecture.

Firmware and schemas outrank clients when they disagree.

## Architecture and ownership

`Channel` owns one lifecycle, one dispatcher, one sequential connection
attempt at a time, pending local-acceptance state, two bounded caches, and the
channel-wide physical-send gate. A transport instance represents exactly one
attempt; after any terminal error it is closed and discarded.

The private transport boundary passes official `ToRadio`/`FromRadio`
envelopes. One receive may overlap one send, but sends serialize inside the
transport. Transports never reconnect and never create application workers.
The lifecycle creates one attempt context and one cancellation watcher which
closes the transport, then uses cancel/close/join teardown before reconnecting.

`Start` returns without requiring a present radio. Channel instances are
one-shot: after `Stop` or parent cancellation they cannot be restarted.
Reconnect delay is fixed at 10 seconds. Readiness requires the complete
`want_config_id` handshake, non-zero `MyNodeInfo`, usable channel records,
and the matching `config_complete_id`. Repeat it after every reconnect or
`rebooted` notification.

Inbound control processing must never block on the agent. Eligible prompts go
through the 64-entry FIFO to the dispatcher; overflow drops the new prompt,
logs it, and retains the dedup entry. Do not add goroutine-per-packet behavior,
an unbounded queue, or cache cleanup workers.

## Transport invariants

Serial uses 115200 baud, a 32-byte `0xC3` wake preamble, drain plus 100 ms
settle, then frames `0x94 0xC3`, big-endian uint16 length, protobuf body.
Reject bodies over 512 bytes and resynchronize after garbage/false headers.
The five-second timer is an inter-byte timeout for a partial frame, not an idle
disconnect. A 300-second channel-owned heartbeat sends a heartbeat envelope.

`go.bug.st/serial` cannot context-cancel a pathological OS-level open,
write, or drain. Keep those operations synchronous. Never “fix” this with an
abandonable helper goroutine; that leaks a late port or write. Context is
checked before/after open and partial writes, and `Close` is the best-effort
interrupt.

HTTP uses plain `http://HOST[:PORT]`, `PUT /api/v1/toradio`, and
`GET /api/v1/fromradio?all=false`. It rejects redirects and bodies over 512
bytes. A successful empty GET waits three seconds internally and polls again;
a non-empty body drains immediately. Do not add probe requests or `all=true`.
Every request and active receive must remain context-cancellable.

A send error records whether zero bytes were definitely written. That bit is
essential: zero-byte failure may wait for reconnection and reconstruct safely;
possible-byte failure is ambiguous and permanent to prevent duplicate LoRa
traffic.

## Routing, triggers, and identity

Canonical nodes are lowercase `!%08x`. DMs use the peer node ID as ChatID;
broadcast sessions use `channel:N`. Group filtering uses configured channel
indices; DMs preserve their actual device channel when replying.

Only decoded, valid UTF-8, non-emoji `TEXT_MESSAGE_APP` packets addressed to
our node or broadcast are chat input. Drop zero IDs, self packets, unavailable
channels, disallowed senders, duplicates, and text received before readiness.

Group trigger precedence is native reply to a recent accepted bot group packet,
literal `@ShortName` mention, configured prefix, then permissive group
fallback. A reply to another user's packet is not a bot trigger. Remove the
first configured prefix and first literal bot mention from the prompt after
classification.

Inbound `MessageID` is the current packet ID;
`ReplyToMessageID` is its decoded `reply_id`. Agent integration deliberately
preserves the inbound context into the final outbound so `parseReplyID` can
use the current user packet ID for a native response. Do not confuse those two
IDs. All physical chunks of one response carry the same native reply target.

`allow_from` is not authentication. Only a firmware-reported successful PKI
decrypt is separately exposed as `meshtastic_pki_encrypted=true`.

## Packet sizing and chunking

All limits are bytes. Normalize as `strings.Join(strings.Fields(text), " ")`.
Greedily pack tokens, keep an over-soft-limit token whole if it physically
fits, and otherwise hard-split only at UTF-8 boundaries. Multi-chunk payloads
use exact `[i/N] ` prefixes determined by a fixed-point count pass. The
implementation scans repeatedly to keep memory bounded; do not replace it with
retained token/chunk slices on low-resource targets.

Physical sizing uses the real generated protobuf runtime and must account for:

- `Data.payload <= 233`;
- encoded Data plus 16-byte header <= 255 for broadcast;
- the same plus 12-byte PKI reserve <= 255 for unicast;
- firmware-added optional bitfield presence;
- actual protobuf overhead from port, payload length, and `reply_id`.

The soft default is 200 bytes. PicoClaw's generic MaxMessageLength counts runes,
so this channel intentionally leaves it zero and owns chunking. At most eight
physical chunks are emitted; an over-limit response is replaced by the
single bounded notice in `oversizedNotice`.

Revalidate every constant against firmware before changing the firmware target.

## Local acceptance and retry safety

Register the pending packet ID before transport I/O. QueueStatus
`res == 0` starts the four-second quiet-rejection window (HTTP poll interval
plus one second). A matching valid local text echo plus QueueStatus accepts
immediately in either order. Real firmware 2.7.18/2.7.22 normally does not echo
host-originated packets; Portduino SimRadio does. Never require an echo.

Local `ROUTING_APP` errors from node zero/our node can reject the live waiter.
The early firmware text-rate path can emit queue success followed by
`RATE_LIMIT_EXCEEDED`, which is why QueueStatus alone is not immediate
success. Mesh ACK/NAK from a peer is not this channel's acceptance boundary.

Only definite queue-full (`res == 32 && free == 0`), target rate, target duty,
or proven zero-byte transport results are internally reconstructable with a
fresh random packet ID. Possible-byte failure, acceptance timeout, unknown
local rejection, or teardown after possible I/O is ambiguous/permanent. Do not
let PicoClaw's manager retry those and duplicate radio traffic.

The acceptance deadline is 60 seconds from waiter registration. Packet IDs are
non-zero crypto-random uint32 values. Only locally accepted group IDs enter the
500-entry/60-minute native-reply cache. Inbound dedup is 1000 entries/5 minutes.
Both are per instance, bounded insertion-order maps/rings with lazy expiry.

Text submissions are serialized and paced at least two seconds apart.
Handshake and heartbeat bypass text pacing. Multi-chunk sends accept each chunk
before sending the next and return the accepted-ID prefix on partial failure.

## Tests and diagnosis

The package tests cover settings/registration, lifecycle, handshake, Serial
framing/resync/cancellation limits, HTTP methods/body limits/cancellation,
routing and metadata, triggers, exact sizing/chunking, acceptance ordering,
quiet-window behavior, pacing, caches, and races. Keep tests hardware- and
network-independent.

Useful focused commands in an installed clean checkout:

```sh
go test ./pkg/channels/meshtastic
go test -race ./pkg/channels/meshtastic
go test ./pkg/agent -run Meshtastic
go test -race ./pkg/agent -run '^TestPublishResponseForInboundIfNeeded_PreservesMessageID$'
```

When a send appears stuck, identify the layer before editing:

1. transport open/request/frame;
2. handshake/readiness and channel role;
3. Phone API QueueStatus/local routing/client notification;
4. LoRa mesh ACK/retry/delivery;
5. PicoClaw inbound trigger/allow rules;
6. agent/provider response;
7. outbound context/reply routing.

Portduino simulator echoes and physical firmware does not; this is expected,
not evidence of a receive bug. Unicast channel zero may be replaced by firmware
from NodeDB; do not work around it by mutating radio state. Never use MQTT as
proof of LoRa delivery.

No physical test may update firmware or persistently change radio/channel
configuration without operator approval. Sanity checks should use existing
configuration and restore only ephemeral processes/files.

## Licensing

The generated Meshtastic Go definitions are GPL-3.0-only. Keep this project
GPL-3.0-only and preserve `LICENSE`, `LICENSES/`, and
`docs/licensing.md`. Do not replace the generated module with handwritten or
obsolete protocol code to seek a permissive license.
