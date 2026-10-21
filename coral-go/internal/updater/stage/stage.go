// Package stage is coral-update's check, download and stage step (#5292): it finds the newest promoted release, has the verifier approve it,
// downloads the package named by the verified sums file into <data dir>/updates/staging/<version>/ and records what it staged. It applies nothing.
// It is linked only into cmd/coral-update (a test holds that), because it uses the Sigstore verifier.
package stage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/subgentic/coral/internal/updater/verify"
)

// The files a staged release is made of: everything verify.Release needs at apply, so apply verifies again from these and trusts no record.
const (
	ManifestFile       = "promote.json"
	ManifestBundleFile = "promote.json.sigstore.json"
	SumsFile           = "SHA256SUMS.txt"
	SumsBundleFile     = "SHA256SUMS.txt.sigstore.json"
	RecordFile         = "record.json"
	UpdatesDir         = "updates"
	StagingDir         = "staging"

	// GitHub's fixed endpoints. "latest/download" redirects to the newest release that is not a prerelease (the same answer the update check
	// uses), so no API host is needed; the manifest it serves names the tag every other URL is built from.
	LatestBase   = "https://github.com/" + verify.Repo + "/releases/latest/download/"
	DownloadBase = "https://github.com/" + verify.Repo + "/releases/download/"

	// DefaultMaxPackage is the hard limit on a package: the sums file carries no size, so a limit per platform stands in for it.
	DefaultMaxPackage = 512 << 20
)

// What a check ended as (the JSON "result"; display only).
const (
	ResultUpToDate      = "up_to_date"
	ResultStaged        = "staged"
	ResultAlreadyStaged = "already_staged"
	ResultNotPromoted   = "not_promoted"
	ResultBackoff       = "backoff"
	ResultBusy          = "busy"
	ResultApplying      = "applying"
	ResultFailed        = "failed"
)

// Result is one check's outcome. It holds no path, URL or hash.
type Result struct {
	Result      string `json:"result"`
	Installed   string `json:"installed"`
	Version     string `json:"version,omitempty"`
	FailedStage string `json:"failed_stage,omitempty"`
	NextAttempt string `json:"next_attempt,omitempty"`
}

// Config is what a check needs; coral-update fills it with the compiled-in values, a test with its own server and signer.
type Config struct {
	DataDir   string
	Installed string
	Pins      verify.Pins
	Verifier  verify.BundleVerifier
	Fetcher   *verify.Fetcher
	// LatestBase and DownloadBase default to GitHub's.
	LatestBase, DownloadBase string
	// Asset names the package for this platform in the sums file.
	Asset func(version string) string
	Now   func() time.Time
	// MaxPackage is the hard size limit (DefaultMaxPackage); BytesPerSecond paces the download (0: unpaced).
	MaxPackage     int64
	BytesPerSecond int64
}

func (c *Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Config) defaults() {
	if c.LatestBase == "" {
		c.LatestBase = LatestBase
	}
	if c.DownloadBase == "" {
		c.DownloadBase = DownloadBase
	}
	if c.MaxPackage <= 0 {
		c.MaxPackage = DefaultMaxPackage
	}
	if c.Fetcher == nil {
		c.Fetcher = &verify.Fetcher{}
	}
}

func notFound(err error) bool {
	var se *verify.StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

// Check runs one check. It never installs and never returns an error: a failure is a Result, and its reason is in the state for Settings.
func Check(ctx context.Context, cfg Config) Result {
	cfg.defaults()
	res := Result{Installed: cfg.Installed}
	now := cfg.now()
	if _, err := verify.ParseVersion(cfg.Installed); err != nil {
		res.Result = ResultFailed
		res.FailedStage = "installed_version"
		return res
	}
	dir := filepath.Join(cfg.DataDir, UpdatesDir)
	if err := verify.EnsureDir(dir); err != nil {
		// The result names only the stage; the reason goes to the log (stderr), so an owner can see why, e.g. a data folder on a volume
		// without access lists, which can't be made private (#126 F8 review).
		log.Printf("[update] the updates folder can't be used: %v", err)
		res.Result, res.FailedStage = ResultFailed, "updates_folder"
		return res
	}
	if applyRunning(dir, now) {
		res.Result = ResultApplying
		return res
	}
	release, err := takeLock(dir, CheckLockFile, now)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			res.Result = ResultBusy
		} else {
			res.Result, res.FailedStage = ResultFailed, "lock"
		}
		return res
	}
	defer release()

	st, _ := verify.LoadState(dir) // an unreadable state is a first run, never permission (the verifier's rules still apply)
	if s := st.Stage; s != nil && s.NextAttempt != "" {
		if next, perr := time.Parse(time.RFC3339, s.NextAttempt); perr == nil && now.Before(next) {
			res.Result, res.Version, res.NextAttempt = ResultBackoff, s.Version, s.NextAttempt
			return res
		}
	}
	c := &checker{cfg: cfg, dir: dir, st: st, now: now, res: res}
	return c.run(ctx)
}

type checker struct {
	cfg Config
	dir string
	st  verify.State
	now time.Time
	res Result
}

func (c *checker) save() {
	c.st.LastCheck = c.now.UTC().Format(time.RFC3339)
	_ = verify.SaveState(c.dir, c.st)
}

// fail records a failure for version and its back-off.
func (c *checker) fail(version, kind, stageName string) Result {
	s := c.st.Stage
	if s == nil || s.Version != version {
		s = &verify.StageState{Version: version, Staged: stageField(c.st), StagedAt: stagedAtField(c.st)}
	}
	s.Attempts++
	s.ErrorKind = kind
	s.NextAttempt = c.now.Add(backoff(kind, s.Attempts)).UTC().Format(time.RFC3339)
	c.st.Stage = s
	c.save()
	c.res.Result, c.res.Version, c.res.FailedStage, c.res.NextAttempt = ResultFailed, version, stageName, s.NextAttempt
	return c.res
}

func stageField(st verify.State) string {
	if st.Stage != nil {
		return st.Stage.Staged
	}
	return ""
}

func stagedAtField(st verify.State) string {
	if st.Stage != nil {
		return st.Stage.StagedAt
	}
	return ""
}

func (c *checker) refused(version string, err error) Result {
	var f *verify.Failure
	if errors.As(err, &f) {
		c.st.Refuse(version, f, c.now)
		return c.fail(version, KindVerify, f.Stage)
	}
	return c.fail(version, KindNetwork, "download")
}

func (c *checker) get(ctx context.Context, url string, max int64) ([]byte, error) {
	return c.cfg.Fetcher.Get(ctx, url, max)
}

func (c *checker) run(ctx context.Context) Result {
	cfg := c.cfg
	// The manifest of the newest release; its tag is where the rest comes from.
	manifest, err := c.get(ctx, cfg.LatestBase+ManifestFile, verify.MaxManifestBytes)
	if notFound(err) {
		c.save()
		c.res.Result = ResultNotPromoted
		return c.res
	}
	if err != nil {
		return c.fail("", KindNetwork, "manifest")
	}
	m, err := verify.ParseManifest(manifest)
	if err != nil {
		return c.fail("", KindVerify, verify.StageManifestDocument)
	}
	c.res.Version = m.Version
	if cmp, cerr := verify.CompareVersions(m.Version, cfg.Installed); cerr != nil || cmp <= 0 {
		c.save()
		c.res.Result = ResultUpToDate
		return c.res
	}
	base := cfg.DownloadBase + m.Tag + "/"
	files := map[string][]byte{ManifestFile: manifest}
	for _, f := range []struct {
		name string
		max  int64
	}{{ManifestBundleFile, verify.MaxBundleBytes}, {SumsFile, verify.MaxSumsBytes}, {SumsBundleFile, verify.MaxBundleBytes}} {
		b, err := c.get(ctx, base+f.name, f.max)
		if notFound(err) {
			c.save()
			c.res.Result = ResultNotPromoted
			return c.res
		}
		if err != nil {
			return c.fail(m.Version, KindNetwork, f.name)
		}
		files[f.name] = b
	}
	cand := verify.Candidate{Manifest: manifest, ManifestBundle: files[ManifestBundleFile], Sums: files[SumsFile], SumsBundle: files[SumsBundleFile]}
	rec, err := verify.Release(cfg.Verifier, cfg.Pins, cand, cfg.Asset, c.st, cfg.Installed, c.now)
	if err != nil {
		return c.refused(m.Version, err)
	}
	c.st.Observe(rec) // a verified manifest's revocations count from now, not only once it is installed (#5301)
	if c.alreadyStaged(rec) {
		c.st.Stage = &verify.StageState{Staged: rec.Version, StagedAt: stagedAtField(c.st)}
		c.save()
		c.res.Result = ResultAlreadyStaged
		return c.res
	}
	if err := c.stage(ctx, base, rec, cand); err != nil {
		return c.refused(m.Version, err)
	}
	c.st.LastFailure = nil
	c.st.Stage = &verify.StageState{Staged: rec.Version, StagedAt: c.now.UTC().Format(time.RFC3339)}
	c.save()
	c.res.Result = ResultStaged
	return c.res
}

// alreadyStaged reports whether what is staged is this very release, with its package intact: the record matches and the package hashes to it.
func (c *checker) alreadyStaged(rec *verify.Record) bool {
	s, err := LoadStaged(c.cfg.DataDir)
	if err != nil || s.Record.Version != rec.Version || s.Record.PackageSHA256 != rec.PackageSHA256 || s.Record.SumsSHA256 != rec.SumsSHA256 {
		return false
	}
	f, err := OpenPlain(s.PackagePath)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = rec.CheckPackage(f, c.cfg.MaxPackage)
	return err == nil
}

// pacer limits a copy to about perSec bytes a second.
type pacer struct {
	w      io.Writer
	perSec int64
	start  time.Time
	n      int64
	ctx    context.Context
}

func (p *pacer) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if p.perSec > 0 {
		due := time.Duration(float64(p.n) / float64(p.perSec) * float64(time.Second))
		if wait := due - time.Since(p.start); wait > 0 {
			select {
			case <-time.After(wait):
			case <-p.ctx.Done():
				return n, p.ctx.Err()
			}
		}
	}
	return n, err
}

type hashWriter struct {
	w io.Writer
	h hash.Hash
}

func (h *hashWriter) Write(b []byte) (int, error) {
	n, err := h.w.Write(b)
	h.h.Write(b[:n])
	return n, err
}

// stage writes the files and the package into a partial folder, checks the package hash as it was written, and moves the folder into place in
// one rename. Older staged versions and leftovers are removed only after that.
func (c *checker) stage(ctx context.Context, base string, rec *verify.Record, cand verify.Candidate) error {
	root := filepath.Join(c.dir, StagingDir)
	if err := verify.EnsureDir(root); err != nil {
		return err
	}
	partial := filepath.Join(root, "."+rec.Version+".partial")
	if err := os.RemoveAll(partial); err != nil {
		return err
	}
	if err := os.Mkdir(partial, 0o700); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(partial)
		}
	}()
	for name, b := range map[string][]byte{ManifestFile: cand.Manifest, ManifestBundleFile: cand.ManifestBundle, SumsFile: cand.Sums, SumsBundleFile: cand.SumsBundle} {
		if err := verify.WritePrivate(filepath.Join(partial, name), b); err != nil {
			return err
		}
	}
	// The package: named by the verified sums entry (ParseSums refuses a separator in it), streamed, hashed as it is written.
	pkg, err := os.OpenFile(filepath.Join(partial, rec.Asset), os.O_WRONLY|os.O_CREATE|os.O_EXCL, verify.PrivateMode)
	if err != nil {
		return err
	}
	h := sha256.New()
	w := &hashWriter{w: &pacer{w: pkg, perSec: c.cfg.BytesPerSecond, start: time.Now(), ctx: ctx}, h: h}
	cerr := c.cfg.Fetcher.Copy(ctx, base+rec.Asset, w, c.cfg.MaxPackage)
	if cerr == nil {
		cerr = pkg.Sync()
	}
	if clerr := pkg.Close(); cerr == nil {
		cerr = clerr
	}
	if cerr != nil {
		return cerr
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rec.PackageSHA256 {
		return &verify.Failure{Stage: verify.StagePackage, Reason: fmt.Sprintf("%s is %s, the verified sums file says %s", rec.Asset, got, rec.PackageSHA256)}
	}
	recJSON, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := verify.WritePrivate(filepath.Join(partial, RecordFile), recJSON); err != nil {
		return err
	}
	final := filepath.Join(root, rec.Version)
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(partial, final); err != nil {
		return err
	}
	done = true
	c.prune(root, rec.Version)
	return nil
}

// prune removes every other entry of the staging folder: older versions, other partials.
func (c *checker) prune(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != keep {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}
