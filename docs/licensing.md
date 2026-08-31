# Licensing review

Reviewed 2026-08-31 against the exact versions in
[`integration/baseline.env`](../integration/baseline.env). This records the
technical licensing basis for publication; it does not replace legal advice for
a particular distribution.

## Verified inputs

- PicoClaw baseline
  [`bbf6893ca7afad27f1d00a0f5a45982a549c6ed6`](https://github.com/sipeed/picoclaw/tree/bbf6893ca7afad27f1d00a0f5a45982a549c6ed6)
  ships an [MIT license](https://github.com/sipeed/picoclaw/blob/bbf6893ca7afad27f1d00a0f5a45982a549c6ed6/LICENSE).
- The official Meshtastic protobuf schemas at the inspected commit
  [`ef0ae579e9de937f7fae25f9fdc5dbfe24a2fe2b`](https://github.com/meshtastic/protobufs/tree/ef0ae579e9de937f7fae25f9fdc5dbfe24a2fe2b)
  ship the GNU GPL version 3 license. The exact downloaded BSR-generated module
  `buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go@v1.36.12-20260811120449-6b706c172b5b.1`
  also contains that same GPLv3 `LICENSE`; it contains no “or any later
  version” grant. Its generated `.pb.go` files are the protocol types linked
  by this channel.
- The BSR module's own `go.mod` requires
  `google.golang.org/protobuf v1.36.12`. That runtime's
  [v1.36.12 license](https://github.com/protocolbuffers/protobuf-go/blob/v1.36.12/LICENSE)
  is BSD-3-Clause.
- `go.bug.st/serial v1.8.0` ships a
  [BSD-3-Clause license](https://github.com/bugst/go-serial/blob/v1.8.0/LICENSE).
- No other module is introduced by the channel. The serial module requires
  `golang.org/x/sys v0.43.0`, but the supported PicoClaw baseline already
  directly resolves `golang.org/x/sys v0.46.0`; the generated integration
  patch therefore adds no x/sys dependency or checksum.

The exact module manifests, module checksums, and generated patch are retained
as review evidence in this repository. Copies of the applicable MIT and BSD
license texts are in [`LICENSES/`](../LICENSES/README.md).

## Result

The Meshtastic generated definitions are the technically correct maintained
protocol dependency and are GPL-3.0-only. This project deliberately does not
replace them with handwritten messages, an obsolete client, or an unmaintained
implementation. The standalone project is therefore licensed GPL-3.0-only.
MIT, BSD-3-Clause, and GPL-3.0-only are compatible for this combination, with
the distributed combined work governed by GPL-3.0-only.

The repository does not vendor PicoClaw or the generated protobuf module.
Ordinary channel source is reviewable under `src/`; Go fetches the exact
generated module during integration/build.

## Obligations by distribution form

### Standalone source repository

Publish the project under GPL-3.0-only, keep the root `LICENSE`, preserve
copyright/license notices, and provide the third-party notices. The exact BSR
module is a build dependency rather than vendored content, but its GPL license
determines the selected license for this purpose-built integration.

### Integration patch

The patch contains original integration work plus small portions/context from
MIT-licensed PicoClaw. It is distributed under GPL-3.0-only with the PicoClaw
MIT notice preserved in this project. Applying it does not remove PicoClaw's
own root `LICENSE`; original PicoClaw material remains available under MIT,
while the combined Meshtastic-enabled work is distributed under GPL-3.0-only.

### Compiled Meshtastic-enabled PicoClaw binary

A binary links PicoClaw, the channel, the GPL generated Meshtastic definitions,
the BSD protobuf runtime, and the BSD serial library into one executable.
Distribute that combined executable under GPL-3.0-only and accompany it with
the complete machine-readable Corresponding Source, or provide it using one of
the methods permitted by GPLv3 section 6. Corresponding Source must be
sufficient to rebuild the exact binary and should include:

- the exact PicoClaw source baseline and any other source used for the build;
- this project's source, integration patch, and build/install scripts;
- the exact dependency versions and checksums;
- all required license and copyright notices.

Binary documentation or other distribution materials must also reproduce the
BSD-3-Clause notices for `go.bug.st/serial` and
`google.golang.org/protobuf`. Do not describe an integrated binary as merely
MIT-licensed PicoClaw.
