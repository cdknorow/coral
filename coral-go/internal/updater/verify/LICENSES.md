# Licences of what only `coral-update` links (Security, #5279)

The Sigstore verifier (`github.com/sigstore/sigstore-go` v1.3.0, Apache-2.0) and the modules only it brings in are linked into `coral-update` and nowhere else (`internal/updater/verify`; `TestSigstoreIsLinkedOnlyIntoTheUpdater` holds that). The package's notices are made from the binaries it ships (`installers/third-party-notices.sh`, checked by `tests/release/verify_notices.sh`): adding `coral-update` to `tests/release/commands.txt` gives every module below its licence text and its row in `THIRD_PARTY_NOTICES.md` with no further step. This file is the review of what those 64 modules are: **50 Apache-2.0, 8 MIT, 6 BSD; no MPL, GPL, LGPL or unlicensed module** (each module's licence file was read and classified when the dependency was added). Re-check it when `go.mod` changes the set. Existing programs' dependencies are unchanged except `golang.org/x/exp` (BSD), which moves to a newer snapshot because sigstore-go requires it.

| Module | Version | Licence | Files |
|---|---|---|---|
| github.com/asaskevich/govalidator | v0.0.0-20230301143203-a9d515a09cc2 | MIT | LICENSE |
| github.com/blang/semver | v3.5.1+incompatible | MIT | LICENSE |
| github.com/cenkalti/backoff/v5 | v5.0.3 | MIT | LICENSE |
| github.com/cespare/xxhash/v2 | v2.3.0 | MIT | LICENSE.txt |
| github.com/cyberphone/json-canonicalization | v0.0.0-20241213102144-19d51d7fe467 | Apache-2.0 | LICENSE |
| github.com/digitorus/pkcs7 | v0.0.0-20230818184609-3a137a874352 | MIT | LICENSE |
| github.com/digitorus/timestamp | v0.0.0-20231217203849-220c5c2851b7 | BSD | LICENSE |
| github.com/go-logr/logr | v1.4.4 | Apache-2.0 | LICENSE |
| github.com/go-logr/stdr | v1.2.2 | Apache-2.0 | LICENSE |
| github.com/go-openapi/analysis | v0.25.5 | Apache-2.0 | LICENSE |
| github.com/go-openapi/errors | v0.22.8 | Apache-2.0 | LICENSE |
| github.com/go-openapi/jsonpointer | v1.0.0 | Apache-2.0 | LICENSE, NOTICE |
| github.com/go-openapi/jsonreference | v1.0.0 | Apache-2.0 | LICENSE, NOTICE |
| github.com/go-openapi/loads | v0.25.0 | Apache-2.0 | LICENSE |
| github.com/go-openapi/runtime | v0.33.0 | Apache-2.0 | LICENSE, NOTICE |
| github.com/go-openapi/runtime/server-middleware | v0.30.0 | Apache-2.0 | LICENSE |
| github.com/go-openapi/spec | v0.22.9 | Apache-2.0 | LICENSE, license.go, license_test.go |
| github.com/go-openapi/strfmt | v0.27.0 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag | v0.26.1 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/cmdutils | v0.27.0 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/conv | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/fileutils | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/jsonname | v0.26.1 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/jsonutils | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/loading | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/mangling | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/netutils | v0.27.0 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/pools | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/stringutils | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/typeutils | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/swag/yamlutils | v0.27.3 | Apache-2.0 | LICENSE |
| github.com/go-openapi/validate | v0.26.1 | Apache-2.0 | LICENSE |
| github.com/go-viper/mapstructure/v2 | v2.5.0 | MIT | LICENSE |
| github.com/google/certificate-transparency-go | v1.3.3 | Apache-2.0 | LICENSE |
| github.com/google/go-containerregistry | v0.21.7 | Apache-2.0 | LICENSE |
| github.com/grpc-ecosystem/grpc-gateway/v2 | v2.29.0 | BSD | LICENSE |
| github.com/in-toto/attestation | v1.2.0 | Apache-2.0 | LICENSE |
| github.com/in-toto/in-toto-golang | v0.11.0 | Apache-2.0 | LICENSE |
| github.com/oklog/ulid/v2 | v2.1.1 | Apache-2.0 | LICENSE |
| github.com/opencontainers/go-digest | v1.0.0 | Apache-2.0 | LICENSE, LICENSE.docs |
| github.com/pkg/errors | v0.9.1 | BSD | LICENSE |
| github.com/secure-systems-lab/go-securesystemslib | v0.11.0 | MIT | LICENSE |
| github.com/shibumi/go-pathspec | v1.3.0 | Apache-2.0 | LICENSE |
| github.com/sigstore/protobuf-specs | v0.5.1 | Apache-2.0 | LICENSE |
| github.com/sigstore/rekor-tiles/v2 | v2.3.0 | Apache-2.0 | LICENSE |
| github.com/sigstore/rekor | v1.5.3 | Apache-2.0 | LICENSE |
| github.com/sigstore/sigstore-go | v1.3.0 | Apache-2.0 | LICENSE |
| github.com/sigstore/sigstore | v1.10.8 | Apache-2.0 | LICENSE |
| github.com/sigstore/timestamp-authority/v2 | v2.1.3 | Apache-2.0 | LICENSE |
| github.com/theupdateframework/go-tuf/v2 | v2.4.2 | Apache-2.0 | LICENSE, NOTICE |
| github.com/transparency-dev/formats | v0.1.1 | Apache-2.0 | LICENSE |
| github.com/transparency-dev/merkle | v0.0.2 | Apache-2.0 | LICENSE |
| github.com/youmark/pkcs8 | v0.0.0-20240726163527-a2c0da244d78 | MIT | LICENSE |
| go.opentelemetry.io/auto/sdk | v1.2.1 | Apache-2.0 | LICENSE |
| go.opentelemetry.io/otel | v1.44.0 | Apache-2.0 | LICENSE |
| go.opentelemetry.io/otel/metric | v1.44.0 | Apache-2.0 | LICENSE |
| go.opentelemetry.io/otel/trace | v1.44.0 | Apache-2.0 | LICENSE |
| golang.org/x/mod | v0.41.0 | BSD | LICENSE, PATENTS |
| golang.org/x/net | v0.57.0 | BSD | LICENSE, PATENTS |
| google.golang.org/genproto/googleapis/api | v0.0.0-20260526163538-3dc84a4a5aaa | Apache-2.0 | LICENSE |
| google.golang.org/genproto/googleapis/rpc | v0.0.0-20260523011958-0a33c5d7ca68 | Apache-2.0 | LICENSE |
| google.golang.org/grpc | v1.82.1 | Apache-2.0 | LICENSE, NOTICE.txt |
| google.golang.org/protobuf | v1.36.11 | BSD | LICENSE, PATENTS |
| k8s.io/klog/v2 | v2.140.0 | Apache-2.0 | LICENSE |
