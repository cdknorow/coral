//go:build dev

package config

// AllowInsecureCloud permits a cleartext (http/ws) Cloud Hub URL, and only for
// loopback hosts. It exists for tests and local development in -tags dev builds.
// Release builds compile it to false, and it is deliberately not a flag, an
// environment variable or a setting.
const AllowInsecureCloud = true
