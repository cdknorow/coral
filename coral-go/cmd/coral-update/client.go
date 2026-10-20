package main

import (
	"net/http"
	"time"
)

// newClient is the HTTP client of the updater: the system's TLS roots and proxy settings, and a long overall timeout, because a package is
// large (the check's own context bounds the whole run). The redirect rules are the fetcher's.
func newClient() *http.Client { return &http.Client{Timeout: checkTimeout + time.Minute} }
