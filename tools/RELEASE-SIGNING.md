# Release signing

Release builds (`.github/workflows/release.yml`, triggered by `v*` tags) are signed on macOS always, and on Windows when `WINDOWS_SIGNING=true`. macOS fails the tag build if any required secret is empty; Windows does too once enabled.

## macOS (Developer ID, hardened runtime, notarized)

Secrets (already set on the repository): `MACOS_CERTIFICATE` (base64 of the Developer ID Application `.p12`), `MACOS_CERTIFICATE_PWD`, `MACOS_SIGNING_IDENTITY` (`Developer ID Application: Name (TEAMID)`), `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD` (app-specific password).

Pipeline: sign every binary with the hardened runtime and `entitlements.plist` (`coral-app` uses `entitlements-app.plist`), sign the app, notarize and staple `Coral.app`, build the DMG, sign, notarize and staple the DMG, then verify with `codesign --verify --strict`, `spctl -a`, and `stapler validate` on both the DMG and the app inside it.

## Windows (Azure Trusted Signing, opt-in while Windows is beta)

Signing is controlled by the repository variable `WINDOWS_SIGNING` (Settings > Secrets and variables > Actions > Variables). It is **off by default**.

- **Off** (unset or not `true`): the credential check and all signing and verification steps are skipped, the msi and portable zip are built from unsigned executables (as in v1.3.43), and the job emits a `::warning::` and a job-summary note "Windows packages are UNSIGNED (beta)". `SHA256SUMS.txt` is unchanged.
- **On** (`WINDOWS_SIGNING=true`): fail closed. A missing secret fails the tag build; every `.exe` and the `.msi` are signed (SHA-256, RFC 3161 timestamp) and checked with `signtool verify /pa /v`. The portable zip is built from the signed executables.

To turn it on later:

1. Create a Trusted Signing account in a supported region and complete identity validation; create a Public Trust certificate profile.
2. Create a service principal with the "Trusted Signing Certificate Profile Signer" role on that profile.
3. Set the repo secrets `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `TRUSTED_SIGNING_ENDPOINT` (for example `https://wus2.codesigning.azure.net`, matching the account region), `TRUSTED_SIGNING_ACCOUNT`, `TRUSTED_SIGNING_PROFILE`.
4. Set the variable `WINDOWS_SIGNING` to `true`. Validate with a throwaway tag build (needs operator OK) before a real release.

## Linux

No OS-level signing. The release carries `SHA256SUMS.txt` covering every package. It sits in the same release as the packages, so it detects corruption, not a compromised release: anyone who can replace a package can replace the file.

## Accepted residuals

- The WiX toolset is installed from NuGet at a pinned version (`wix` 5.0.2) but without hash pinning, and `choco install mingw` is unpinned. Both run before signing, so a compromised package would be signed. The Trusted Signing action also fetches the Trusted Signing client tooling from NuGet at run time, unpinned and unverified, and that code handles the signing credentials. Accepted for now.
- The macOS secret check (`Require macOS release signing credentials`) runs at the start of `build-macos` and fails the tag build if any of the six secrets is empty.

## Recommendations for the operator (not yet applied)

1. **GitHub Environment `release`.** Put the signing steps behind an Environment named `release` with required reviewers, and add a tag ruleset restricting who can create `v*` tags. Move the Apple and Azure secrets into that Environment so a branch or fork workflow can never read them.
2. **OIDC instead of `AZURE_CLIENT_SECRET`.** Create a federated credential on the service principal (subject `repo:subgentic/coral:environment:release`) and grant the job `id-token: write`. This removes the long-lived Azure secret. Not configured yet: Windows stays unsigned for now, so no Azure credential exists.
3. **Action migration.** `azure/trusted-signing-action` has been renamed to `Azure/artifact-signing-action`. Plan to move to it (pinned by SHA) once its inputs are confirmed on the throwaway signtest tag build, which needs operator OK and the Azure secrets first.
