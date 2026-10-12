# Release signing

Release builds (`.github/workflows/release.yml`, triggered by `v*` tags) are signed on every platform. A tag build **fails** if any required secret is empty; it never ships unsigned.

## macOS (Developer ID, hardened runtime, notarized)

Secrets (already set on the repository): `MACOS_CERTIFICATE` (base64 of the Developer ID Application `.p12`), `MACOS_CERTIFICATE_PWD`, `MACOS_SIGNING_IDENTITY` (`Developer ID Application: Name (TEAMID)`), `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD` (app-specific password).

Pipeline: sign every binary with the hardened runtime and `entitlements.plist` (`coral-app` uses `entitlements-app.plist`), sign the app, notarize and staple `Coral.app`, build the DMG, sign, notarize and staple the DMG, then verify with `codesign --verify --strict`, `spctl -a`, and `stapler validate` on both the DMG and the app inside it.

## Windows (Azure Trusted Signing)

Secrets to create: `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET` (a service principal with the "Trusted Signing Certificate Profile Signer" role on the certificate profile), `TRUSTED_SIGNING_ENDPOINT` (for example `https://wus2.codesigning.azure.net`, matching the account region), `TRUSTED_SIGNING_ACCOUNT`, `TRUSTED_SIGNING_PROFILE`.

Every `.exe` in the bundle and the `.msi` are signed with SHA-256 and an RFC 3161 timestamp, then checked with `signtool verify /pa /v`. The portable zip is built from the signed executables.

## Linux

No OS-level signing. The release carries `SHA256SUMS.txt` covering every package.
