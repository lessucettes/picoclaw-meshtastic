# Third-party license notices

This directory preserves the license texts relevant to the integration and its
directly introduced dependencies.

| Component | Exact version/baseline | License | Notice file |
|---|---|---|---|
| PicoClaw | `bbf6893ca7afad27f1d00a0f5a45982a549c6ed6` | MIT | [PicoClaw-MIT.txt](PicoClaw-MIT.txt) |
| Meshtastic generated protobuf Go module | `v1.36.12-20260811120449-6b706c172b5b.1` | GPL-3.0-only | The project root [LICENSE](../LICENSE) is the identical license text shipped by the module |
| go.bug.st/serial | `v1.8.0` | BSD-3-Clause | [go-serial-BSD-3-Clause.txt](go-serial-BSD-3-Clause.txt) |
| google.golang.org/protobuf | `v1.36.12` | BSD-3-Clause | [protobuf-go-BSD-3-Clause.txt](protobuf-go-BSD-3-Clause.txt) |

The standalone repository does not vendor third-party Go module source. Go
retrieves the exact versions recorded in the generated PicoClaw `go.mod` and
`go.sum`.
