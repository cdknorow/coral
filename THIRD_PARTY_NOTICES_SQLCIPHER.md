# Third-party notices for encrypted database builds

Coral encrypted binaries include or link the following components. Preserve
this notice with packaged binaries and application bundles.

## 0xCarbon/go-sqlite3 v1.15.1

The Go binding is MIT licensed. The exact module license is distributed at
`github.com/0xCarbon/go-sqlite3@v1.15.1/LICENSE` and the source checksum is
recorded in `coral-go/go.sum`.

## SQLCipher 4.12.0 and SQLite 3.51.1

The bundled amalgamation contains SQLCipher/Zetetic BSD-style notices and
SQLite public-domain notices in `sqlite3-binding.c`. Source and license text:

- https://github.com/sqlcipher/sqlcipher
- https://github.com/sqlcipher/sqlcipher/blob/master/LICENSE.md
- https://github.com/sqlcipher/sqlcipher/blob/master/SQLITE_LICENSE.md

## OpenSSL

Release artifacts dynamically link a copied OpenSSL `libcrypto` library.
The verified release builds package these versions:

- Linux: Ubuntu's OpenSSL 3.0.13-0ubuntu3.16 `libcrypto.so.3`.
- Windows: Chocolatey's OpenSSL 4.0.3 `libcrypto-4-x64.dll`.
- macOS: OpenSSL Project 3.6.5 `libcrypto.3.dylib`, built for arm64 and x86_64.

The OpenSSL Project licenses these releases under Apache License 2.0. Its
complete `LICENSE.txt` is included as `licenses/OPENSSL-LICENSE.txt` in each
package and in `Coral.app/Contents/Resources/licenses/`. The upstream READMEs
carry these copyright notices:

OpenSSL 3.0.13:

Copyright (c) 1998-2024 The OpenSSL Project

Copyright (c) 1995-1998 Eric A. Young, Tim J. Hudson

OpenSSL 3.6.5 and 4.0.3:

Copyright (c) 1998-2026 The OpenSSL Project Authors

Copyright (c) 1995-1998 Eric A. Young, Tim J. Hudson

Upstream sources and license text for the packaged versions:

- https://github.com/openssl/openssl/tree/openssl-3.0.13
- https://github.com/openssl/openssl/tree/openssl-3.6.5
- https://github.com/openssl/openssl/tree/openssl-4.0.3

Release validation must verify that the packaged OpenSSL version matches the
runner's security updates and must not claim zero known vulnerabilities.
