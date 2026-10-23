package stage

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/subgentic/coral/internal/updater/verify"
)

// The Windows rollback package (#162 (b)): on an MSI install, the check also keeps the INSTALLED version's own MSI, fetched from its release and
// verified with the same chain as an update (verify.ReleaseOf), in updates/rollback/<version>/, laid out like a staged release. The apply puts
// it back when a new version didn't come up (apply.RunMSI's rollback), after verifying it again where it is used (ReverifyRollback: the
// folder is user-writable and the copy may sit there for days). Once per installed version: a copy that still verifies is kept, other
// versions are removed. A failure here never stops the update being staged; the apply then has no rollback package and says so.

// RollbackDir is the folder under the updates folder that holds the rollback package.
const RollbackDir = "rollback"

// keepRollbackIfAsked keeps the rollback package when the configuration asks for it, logging (never failing the check) when it can't.
func (c *checker) keepRollbackIfAsked(ctx context.Context) {
	if !c.cfg.Rollback {
		return
	}
	if err := c.keepRollback(ctx); err != nil {
		log.Printf("[update] the installed version's package for the rollback could not be kept: %v", err)
	}
}

// keepRollback fetches and verifies the installed version's release into the rollback folder, unless a copy of it there still verifies.
func (c *checker) keepRollback(ctx context.Context) error {
	version := c.cfg.Installed
	root := filepath.Join(c.dir, RollbackDir)
	if err := verify.EnsureDir(root); err != nil {
		return err
	}
	if s, err := LoadRollback(c.cfg.DataDir, version); err == nil {
		if _, err := ReverifyRollback(c.cfg.Verifier, c.cfg.Pins, s, c.cfg.Asset, version, c.st, c.now, c.cfg.MaxPackage); err == nil {
			c.prune(root, version) // other versions go; this one is kept
			return nil
		}
		// A kept copy that no longer verifies (a later release revoked the version, or the files changed) goes, so nothing stale is left to
		// find; a fresh copy is fetched below, and verified again.
		_ = os.RemoveAll(s.Dir)
	}
	base := c.cfg.DownloadBase + "v" + version + "/"
	// The installed version's own promotion (its manifest is a release of its own since #6393), found for exactly its tag.
	p, _, perr := c.findPromotion(ctx, "v"+version)
	if perr != nil {
		return fmt.Errorf("promotion of v%s: %w", version, perr)
	}
	files := map[string][]byte{ManifestFile: p.manifest, ManifestBundleFile: p.bundle}
	for _, f := range []struct {
		name string
		max  int64
	}{{SumsFile, verify.MaxSumsBytes}, {SumsBundleFile, verify.MaxBundleBytes}} {
		b, err := c.get(ctx, base+f.name, f.max)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		files[f.name] = b
	}
	cand := verify.Candidate{Manifest: files[ManifestFile], ManifestBundle: files[ManifestBundleFile], Sums: files[SumsFile], SumsBundle: files[SumsBundleFile]}
	rec, err := verify.ReleaseOf(c.cfg.Verifier, c.cfg.Pins, cand, c.cfg.Asset, version, c.st, c.now)
	if err != nil {
		return err
	}
	return c.stageInto(ctx, root, base, rec, cand) // the package hashed as it is written; other versions removed after the rename
}

// LoadRollback reads the rollback package kept for version: ErrNoStaged when there is none, or it is for another version.
func LoadRollback(dataDir, version string) (*Staged, error) {
	s, err := loadStagedIn(filepath.Join(dataDir, UpdatesDir, RollbackDir))
	if err != nil {
		return nil, err
	}
	if s.Version != version {
		return nil, ErrNoStaged
	}
	return s, nil
}

// ReverifyRollback verifies the kept rollback package again, as apply does at use (#162 condition C): the whole chain from its files with the
// re-install rules (exactly version, not revoked), then the package hashed against the verified sums entry. It returns the fresh record.
func ReverifyRollback(v verify.BundleVerifier, pins verify.Pins, s *Staged, asset func(string) string, version string, st verify.State, now time.Time, maxPackage int64) (*verify.Record, error) {
	if maxPackage <= 0 {
		maxPackage = DefaultMaxPackage
	}
	rec, err := verify.ReleaseOf(v, pins, s.Candidate, asset, version, st, now)
	if err != nil {
		return nil, err
	}
	if rec.Version != s.Version || filepath.Join(s.Dir, rec.Asset) != s.PackagePath {
		return nil, &verify.Failure{Stage: verify.StagePackage, Reason: "the rollback folder is not the verified release's"}
	}
	f, err := OpenPlain(s.PackagePath)
	if err != nil {
		return nil, &verify.Failure{Stage: verify.StagePackage, Reason: "the rollback package cannot be opened"}
	}
	defer f.Close()
	if _, err := rec.CheckPackage(f, maxPackage); err != nil {
		return nil, err
	}
	return rec, nil
}
