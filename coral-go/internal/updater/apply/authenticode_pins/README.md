# Authenticode pins for the Windows package (#162)

Coral's Windows releases are signed with Azure Trusted Signing. The updater accepts a signed MSI only when WinVerifyTrust passes (with revocation) **and** its chain goes through both certificates below **and** the leaf names `O=Subgentic LLC, CN=Subgentic LLC` (`authenticode_pin.go`). Trusted Signing's leaf certificates last about 3 days and their issuing CAs ("Microsoft ID Verified CS EOC/AOC CA 0x") rotate, so neither is pinned.

| File | Subject | SHA-256 (DER) | Valid | Fetched from |
|---|---|---|---|---|
| `Microsoft_ID_Verified_Code_Signing_PCA_2021.crt` | CN=Microsoft ID Verified Code Signing PCA 2021 (issuer: the root below) | `3d29798cc5d3f0644a7e0dc9cb1cade523ea5ec83b335109b605bfeaa7d5f5c1` | 2021-04-01 to 2036-04-01 | https://www.microsoft.com/pkiops/certs/Microsoft%20ID%20Verified%20Code%20Signing%20PCA%202021.crt |
| `Microsoft_Identity_Verification_Root_Certificate_Authority_2020.crt` | CN=Microsoft Identity Verification Root Certificate Authority 2020 (self-signed) | `5367f20c7ade0e2bca790915056d086b720c33c1fa2a2661acf787e3292e1270` | 2020-04-16 to 2045-04-16 | https://www.microsoft.com/pkiops/certs/Microsoft%20Identity%20Verification%20Root%20Certificate%20Authority%202020.crt |

Both were fetched on 2026-10-22 from Microsoft's PKI repository (https://www.microsoft.com/pkiops/docs/repository.htm). `TestAuthenticodePinsAreTheRecordedCertificates` checks that the hashes in `authenticode_pin.go` are these files' SHA-256.
