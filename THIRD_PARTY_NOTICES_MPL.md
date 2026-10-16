# Third-party notices: MPL-2.0 components

Coral binaries include the following Go modules, which are licensed under the
Mozilla Public License, version 2.0. Each is used unmodified. The license text is
in `licenses/HASHICORP-MPL-2.0.txt`, and the exact source is recorded, with its
checksum, in `coral-go/go.sum`. Preserve this notice with packaged binaries and
application bundles.

| Module | Version | Copyright | Source Code Form |
|---|---|---|---|
| github.com/hashicorp/yamux | v0.1.2 | Copyright (c) 2014 HashiCorp, Inc. | https://github.com/hashicorp/yamux/tree/v0.1.2 |
| github.com/hashicorp/go-version | v1.7.0 | Copyright (c) 2014 HashiCorp, Inc. | https://github.com/hashicorp/go-version/tree/v1.7.0 |
| github.com/hashicorp/golang-lru/v2 | v2.0.7 | Copyright (c) 2014 HashiCorp, Inc. | https://github.com/hashicorp/golang-lru/tree/v2.0.7 |

The Source Code Form of each module is also available from the Go module proxy,
for example `go mod download github.com/hashicorp/yamux@v0.1.2`.

Every other Go module linked into Coral is under a permissive license (MIT,
BSD, Apache-2.0, ISC or similar). The encrypted-database components are listed in
`THIRD_PARTY_NOTICES_SQLCIPHER.md`.
