# picoclaw-meshtastic

Native Meshtastic support for the official
[PicoClaw](https://github.com/sipeed/picoclaw) project.

This is not a PicoClaw fork. It contains the Meshtastic channel as ordinary,
reviewable Go source plus a small generated patch for the places where an
official PicoClaw checkout must register and configure the channel:

```text
official PicoClaw + picoclaw-meshtastic = PicoClaw with native Meshtastic support
```

## Compatibility

The currently supported PicoClaw baseline is exactly:

```text
bbf6893ca7afad27f1d00a0f5a45982a549c6ed6
feat(models): add configurable default fallback chain (#3200)
```

PicoClaw had no release tag at that commit (`nightly-50-gbbf6893c` by
`git describe`). The installer intentionally refuses other commits. Upstream
channel, configuration, MessageBus, and dependency APIs can change; a newer
commit must be reviewed and added as a new supported baseline.

## Install into PicoClaw

Requirements: Git, a Unix-like shell, and the Go version required by the
supported PicoClaw checkout (currently Go 1.25.13).

```sh
git clone https://github.com/sipeed/picoclaw.git
git -C picoclaw checkout bbf6893ca7afad27f1d00a0f5a45982a549c6ed6

git clone https://github.com/lessucettes/picoclaw-meshtastic.git
picoclaw-meshtastic/scripts/install.sh ./picoclaw

cd picoclaw
go build ./cmd/picoclaw
go test ./pkg/channels/meshtastic ./pkg/agent ./pkg/channels ./pkg/config ./pkg/gateway
```

The installer:

- verifies the destination is a Git repository with PicoClaw's module and
  expected source layout;
- reports and checks its exact `HEAD` commit against the supported baseline;
- rejects any non-identical file already present at a channel source path;
- runs `git apply --check` before changing anything;
- refuses an incompatible or overlapping patch instead of forcing it;
- copies the canonical source files and applies the generated integration
  patch without staging, resetting, or discarding unrelated local changes;
- is idempotent when the same integration is already present.

Review the resulting changes with `git status` and `git diff`. Build artifacts
belong in the PicoClaw checkout, not this project.

## Configure

Meshtastic instances use PicoClaw's normal `channel_list` configuration. A
Serial/USB example:

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

For Wi-Fi, use `"transport": "http"` and an `"http_address"` containing only
a host or host:port, for example `meshtastic.local`. The channel uses the
device's HTTP Phone API on plain HTTP; it does not accept a URL scheme or path.

Start PicoClaw normally:

```sh
./picoclaw gateway
```

The complete setting reference, environment variable names, trigger rules, and
security notes are in the
[installed channel guide](src/docs/channels/meshtastic/README.md).

## Supported behavior

- Official Meshtastic Serial Stream API at 115200 baud.
- Meshtastic HTTP Phone API over Wi-Fi.
- Multiple named PicoClaw Meshtastic channel instances.
- Direct messages and selected Primary/Secondary broadcast channels.
- PicoClaw `allow_from`, mentions, prefixes, permissive group triggers, and
  native replies to recent bot packets.
- Native Meshtastic `reply_id` preservation for agent responses.
- Exact UTF-8 byte-aware packet sizing and ordered chunking, with a bounded
  eight-packet response policy.
- Bounded inbound deduplication and native-reply correlation caches.
- Sequential reconnect and complete device handshake after reconnect/reboot.
- Local queue/routing/duty/rate feedback handling without duplicating LoRa
  submissions.
- Bounded internal queues and cancellation-aware lifecycle suitable for
  low-resource Linux/ARM systems.

Host-side MQTT is intentionally not implemented. Traffic that the attached
radio itself received through its configured device MQTT path is still
ordinary Phone API input, but MQTT must not be used to claim a real LoRa test.

## Tests and clean-room verification

Normal tests use fake transports and `httptest`; CI requires no radio.

```sh
./scripts/verify.sh
```

The verifier regenerates the patch and checks it byte-for-byte, obtains a fresh
official PicoClaw checkout at the supported commit, installs this project
exactly as a user would, builds PicoClaw, runs Meshtastic and affected PicoClaw
tests, and runs the relevant race tests. Set `PICOCLAW_REPOSITORY` to a local
PicoClaw Git repository to perform the same clean-checkout workflow without a
second network fetch.

Hardware and meshtasticd checks are release sanity tests, not normal CI.

## Source and patch maintenance

```text
src/pkg/channels/meshtastic/       canonical channel and tests
src/pkg/agent/                     Meshtastic-specific integration test
src/docs/channels/meshtastic/      installed channel guide
tools/edit-integration.go          canonical edits to existing PicoClaw files
integration/baseline.env           upstream/dependency pins
integration/picoclaw-bbf6893c.patch generated review artifact
scripts/install.sh                 safe end-user installation
scripts/generate-patch.sh          deterministic patch regeneration
scripts/verify.sh                  clean-room build/test/race verification
```

Edit the canonical files under `src/` and the anchor-checked integration
transformer. Then run:

```sh
./scripts/generate-patch.sh
./scripts/verify.sh
```

Do not edit the generated patch by hand. Generation starts from a clean
checkout of the exact baseline, installs the real source tree, performs the
canonical existing-file edits, resolves the pinned modules with `go mod tidy`,
and emits a diff restricted to the known PicoClaw integration files.

To support a newer PicoClaw version, first inspect its current native channel
interfaces, configuration factory, gateway registration, MessageBus reply
context, rate limiter, and dependency graph. Update the baseline and
anchor-checked transformations only after adapting the canonical channel;
regenerate the patch and require the full clean-room verifier to pass. Keep the
old patch or add a new baseline only when it can still be verified exactly.

## Known limitations

- Serial and plain HTTP are the only host-device transports in v1; no host
  MQTT, HTTPS, media, reactions, telemetry, traceroute, or device configuration.
- `allow_from` is an application routing filter, not cryptographic sender
  authentication for legacy PSK/public-channel traffic.
- Reply/deduplication caches and queued PicoClaw messages are memory-only.
- Serial open/write/drain cancellation is limited by the selected library and
  operating-system driver behavior; normal reads and HTTP operations are
  cancellable.
- Local send success means the attached firmware accepted the packet into its
  local path. Mesh ACKs, retries, and end-to-end delivery remain firmware
  responsibilities.
- Device firmware can substitute a remembered NodeDB channel for a unicast
  packet submitted with channel index 0. The channel does not mutate radio
  configuration to override that behavior.

See the channel guide for the detailed operational limits.

## License

This project is GPL-3.0-only because the exact maintained generated Meshtastic
protobuf Go module is GPL-3.0-only. PicoClaw is MIT; `go.bug.st/serial` and the
Go protobuf runtime are BSD-3-Clause. These licenses are compatible in the
combined GPL-3.0-only work.

A distributed Meshtastic-enabled PicoClaw binary must be accompanied by the
GPLv3 Corresponding Source obligations and the applicable BSD notices. Read the
[licensing review](docs/licensing.md) and [third-party notices](LICENSES/README.md)
before distributing binaries.
