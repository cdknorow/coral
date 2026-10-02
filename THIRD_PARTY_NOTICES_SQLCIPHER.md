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

Release artifacts dynamically link the platform OpenSSL `libcrypto` library.
The platform package's OpenSSL license and notice files must be included
alongside the copied library. OpenSSL licensing and source are documented at:

- https://www.openssl.org/source/license.html
- https://github.com/openssl/openssl

Release validation must verify that the packaged OpenSSL version matches the
runner's security updates and must not claim zero known vulnerabilities.
