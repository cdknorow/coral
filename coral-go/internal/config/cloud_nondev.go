//go:build !dev

package config

// AllowInsecureCloud is false in every non-dev build: the connector only talks
// to a Cloud Hub over verified TLS. See cloud_dev.go.
const AllowInsecureCloud = false
