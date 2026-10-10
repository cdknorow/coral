// Package frontend exports Coral's web UI assets so other binaries (the Cloud
// Hub) can serve the same frontend as the Coral server. Paths inside the
// filesystems keep their "static/" and "templates/" prefixes.
package frontend

import "embed"

// Static holds the files served under /static/ (JavaScript modules, CSS, vendor
// bundles, icons), rooted at "static/".
//
//go:embed all:static
var Static embed.FS

// Templates holds the Go html/template sources of the single-page app shell,
// rooted at "templates/".
//
//go:embed all:templates
var Templates embed.FS
