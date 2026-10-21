package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	cacheMaxAge       = 50 * time.Minute // a token lives an hour; it is never served after 50 minutes
	cacheExpiryMargin = 2 * time.Minute  // nor within this long of the expiry GitHub states
	maxCacheBytes     = 8 << 10
)

// cacheEntry is one cached token: for one installation (or the lookup), one repository, one permission set.
type cacheEntry struct {
	Token          string    `json:"token"`
	ExpiresAt      time.Time `json:"expires_at"`
	FetchedAt      time.Time `json:"fetched_at"`
	InstallationID int64     `json:"installation_id"`
	Repo           string    `json:"repo"`
	Perms          string    `json:"perms"`
}

// usableUntil is the last moment the cached token is served.
func (e cacheEntry) usableUntil() time.Time {
	until := e.FetchedAt.Add(cacheMaxAge)
	if g := e.ExpiresAt.Add(-cacheExpiryMargin); g.Before(until) {
		until = g
	}
	return until
}

func cacheName(req request) string {
	id := "lookup"
	if req.InstallationID > 0 {
		id = fmt.Sprintf("%d", req.InstallationID)
	}
	return fmt.Sprintf("%s-%s-%s.json", id, req.Repo, req.Perms)
}

// readCache returns the entry for the request when it passes every check; anything else is a miss.
func (a *app) readCache(req request) (cacheEntry, bool) {
	path := a.keys.cachePath(cacheName(req))
	if checkDir(a.keys.cacheDir(), a.keys.uid) != nil {
		return cacheEntry{}, false
	}
	fi, err := os.Lstat(path)
	if err != nil || checkFile(path, fi, a.keys.uid) != nil || fi.Size() > maxCacheBytes {
		return cacheEntry{}, false
	}
	f, err := openNoFollow(path)
	if err != nil {
		return cacheEntry{}, false
	}
	defer f.Close()
	fi2, err := f.Stat()
	if err != nil || !os.SameFile(fi, fi2) || checkFile(path, fi2, a.keys.uid) != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if json.NewDecoder(io.LimitReader(f, maxCacheBytes)).Decode(&e) != nil {
		return cacheEntry{}, false
	}
	now := a.now()
	switch {
	case e.Token == "", e.Repo != req.Repo, e.Perms != req.Perms,
		req.InstallationID > 0 && e.InstallationID != req.InstallationID,
		e.FetchedAt.After(now), // a clock that went backwards, or a hand-edited file
		!now.Before(e.usableUntil()):
		return cacheEntry{}, false
	}
	return e, true
}

// writeCache stores the entry in a 0600 file inside a 0700 folder, replacing any earlier file atomically.
func (a *app) writeCache(req request, e cacheEntry) error {
	dir := a.keys.cacheDir()
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("cache folder: %w", err)
	}
	if err := checkDir(dir, a.keys.uid); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return errors.New("could not encode the cache entry")
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*") // created 0600
	if err != nil {
		return fmt.Errorf("cache file: %w", err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		return errors.New("could not write the cache file")
	}
	if err := os.Rename(tmpName, a.keys.cachePath(cacheName(req))); err != nil {
		os.Remove(tmpName)
		return errors.New("could not place the cache file")
	}
	return nil
}
