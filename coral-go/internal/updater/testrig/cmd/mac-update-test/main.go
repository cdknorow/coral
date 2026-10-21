//go:build darwin

// mac-update-test is the macOS update test's own program (#5581). It is NOT a released program: no list in tests/release names it, and
// testrig's guard test shows no released program links this package. It plays coral-update's part on a Mac with the REAL code of the updater:
// stage.Check (fetch, verify, stage) and apply.Run (verify again, build the new app from the package, check it where it will run, swap, start, health,
// roll back) with the real macOS platform and the real Sigstore verification of a REAL release's SHA256SUMS.txt (the release workflow's identity of the
// pinned repository and its numeric id), the real disk image, the pinned Developer ID team, notarization and version.
//
// The one thing that is not real is the PROMOTE MANIFEST, because no promote workflow exists yet (PromoteRepoID is empty, so the compiled-in pins refuse
// everything, which is correct). Here a manifest is made from the release's own files and signed with an ed25519 key that is generated when this program
// starts, lives in memory only, and is trusted by this program alone: the shipped coral-update, verify.DefaultPins and every release build know nothing
// of it, and a test shows no released program links this one.
//
//	mac-update-test --data-dir D --installed 1.4.100 --release-dir R --tag v1.4.144 --source-sha SHA --parent PID --start-script S [--scenario ...]
//
// R holds the release's SHA256SUMS.txt, SHA256SUMS.txt.sigstore.json and Coral.v<version>.dmg (from the release run's artifacts). The program must run
// from <name>.app/Contents/MacOS/ of the install it replaces (the install is found as the real apply finds it). It prints one JSON line per step.
//
// Scenarios: good (the update), tamper-package (the served disk image has one bit flipped), tamper-staged (the staged package is changed after the check),
// wrong-identity (the release's sums are another identity's: --other-sums-dir has them), unhealthy (the new app is never started, so the apply rolls back).
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// macTeam is the Developer ID team the real v1.4.144 app is signed by (Security's pin note); the real DefaultPins leave MacTeamIDs empty until the updater ships.
const macTeam = "33UR27T84L"

func say(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func main() { os.Exit(run()) }

// testVerifier trusts the test key for the promote manifest's identity and the real Sigstore for everything else (the release's sums).
type testVerifier struct {
	real verify.Sigstore
	pub  ed25519.PublicKey
	pins verify.Pins
}

func (t testVerifier) VerifyBundle(bundleJSON, artifact []byte, id verify.Identity) (verify.Cert, error) {
	if strings.HasPrefix(id.SAN, "https://github.com/"+t.pins.PromoteRepo+"/") {
		var b struct {
			Sig string `json:"test_signature"`
		}
		if err := json.Unmarshal(bundleJSON, &b); err != nil {
			return verify.Cert{}, err
		}
		sig, err := base64.StdEncoding.DecodeString(b.Sig)
		if err != nil || !ed25519.Verify(t.pub, artifact, sig) {
			return verify.Cert{}, errors.New("the test manifest's signature is not valid")
		}
		return verify.Cert{SAN: id.SAN}, nil
	}
	return t.real.VerifyBundle(bundleJSON, artifact, id)
}

// testPlatform is the real macOS platform with one difference: the app is started by the start script (an isolated HOME and data folder, a port of its own,
// never `open`, which would start it as the user's Coral).
type testPlatform struct {
	*apply.Mac
	startScript string
	// skipFirstStart: the first start (the NEW app) does nothing, so it is never healthy; the start of the OLD app after the roll-back runs.
	skipFirstStart bool
	starts         int
}

func (p *testPlatform) Start(ctx context.Context, install string) error {
	p.starts++
	if p.skipFirstStart && p.starts == 1 {
		return nil
	}
	return exec.CommandContext(ctx, "/bin/bash", p.startScript, install).Run()
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func run() int {
	dataDir := flag.String("data-dir", "", "the test Coral's data folder (isolated)")
	installed := flag.String("installed", "", "the installed (older) version")
	relDir := flag.String("release-dir", "", "the real release's SHA256SUMS.txt, its bundle and the disk image")
	tag := flag.String("tag", "", "the release's tag, e.g. v1.4.144")
	sourceSHA := flag.String("source-sha", "", "the commit the tag was built from (40 hex)")
	parent := flag.Int("parent", 0, "the pid of the server to be replaced")
	startScript := flag.String("start-script", "", "the script that starts an install with the isolated environment")
	scenario := flag.String("scenario", "good", "good, tamper-package, tamper-staged, wrong-identity, unhealthy")
	otherSums := flag.String("other-sums-dir", "", "wrong-identity: SHA256SUMS.txt and its bundle of another identity's release")
	mode := flag.String("mode", "both", "check or both")
	hold := flag.Duration("hold", 0, "how long to stay stopped right after the swap, to look at the two bundles")
	waitFor := flag.Duration("wait", 2*time.Minute, "how long the apply waits for the server to end")
	healthFor := flag.Duration("health-for", 30*time.Second, "how long the new install has to stay up")
	flag.Parse()
	if *dataDir == "" || *installed == "" || *relDir == "" || *tag == "" || *sourceSHA == "" || *startScript == "" {
		fmt.Fprintln(os.Stderr, "usage: mac-update-test --data-dir D --installed V --release-dir R --tag vX --source-sha SHA --parent PID --start-script S [--scenario ...] [--mode check|both]")
		return 2
	}
	version := strings.TrimPrefix(*tag, "v")
	asset := func(v string) string { return "Coral.v" + v + ".dmg" }

	sums, err1 := os.ReadFile(filepath.Join(*relDir, "SHA256SUMS.txt"))
	bundle, err2 := os.ReadFile(filepath.Join(*relDir, "SHA256SUMS.txt.sigstore.json"))
	dmg, err3 := os.ReadFile(filepath.Join(*relDir, asset(version)))
	if err := errors.Join(err1, err2, err3); err != nil {
		fmt.Fprintln(os.Stderr, "release files:", err)
		return 2
	}
	if *scenario == "wrong-identity" {
		// the sums of ANOTHER release, signed by another identity, offered as this release's: the manifest names their digest, so only the signature's identity is wrong
		var e1, e2 error
		sums, e1 = os.ReadFile(filepath.Join(*otherSums, "SHA256SUMS.txt"))
		bundle, e2 = os.ReadFile(filepath.Join(*otherSums, "SHA256SUMS.txt.sigstore.json"))
		if err := errors.Join(e1, e2); err != nil {
			fmt.Fprintln(os.Stderr, "other sums:", err)
			return 2
		}
	}
	served := dmg
	if *scenario == "tamper-package" {
		served = append([]byte(nil), dmg...)
		served[len(served)/2] ^= 1
	}

	// The test manifest: made from the release's own files, signed by a key that exists only in this process.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "key:", err)
		return 2
	}
	now := time.Now().UTC()
	manifest, _ := json.Marshal(map[string]any{
		"schema": 1, "repo": verify.Repo, "tag": *tag, "version": version, "prerelease": false, "source_sha": *sourceSHA,
		"sha256sums_sha256": sha(sums), "sequence": 1, "promoted_at": now.Add(-time.Minute).Format(time.RFC3339),
		"expires_at": now.Add(24 * time.Hour).Format(time.RFC3339), "revoked": []string{},
	})
	sigJSON, _ := json.Marshal(map[string]string{"test_signature": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))})

	// A loopback server standing in for github.com: the manifest as "latest", every other file under the tag.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		return 2
	}
	files := map[string][]byte{
		"/latest/" + stage.ManifestFile:                      manifest,
		"/download/" + *tag + "/" + stage.ManifestBundleFile: sigJSON,
		"/download/" + *tag + "/" + stage.SumsFile:           sums,
		"/download/" + *tag + "/" + stage.SumsBundleFile:     bundle,
		"/download/" + *tag + "/" + asset(version):           served,
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	base := "http://" + ln.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	trust, source, err := verify.LoadTrust(ctx, filepath.Join(*dataDir, stage.UpdatesDir, "tuf"))
	if err != nil {
		say(map[string]any{"step": "trusted_root", "result": "failed", "error": err.Error()})
		return 1
	}
	say(map[string]any{"step": "trusted_root", "source": string(source)})

	pins := verify.DefaultPins()
	pins.MacTeamIDs = []string{macTeam}
	verifier := testVerifier{real: verify.Sigstore{Trust: trust}, pub: pub, pins: pins}
	scfg := stage.Config{
		DataDir: *dataDir, Installed: *installed, Pins: pins, Verifier: verifier, Asset: asset,
		LatestBase: base + "/latest/", DownloadBase: base + "/download/",
		Fetcher: &verify.Fetcher{Scheme: "http", Allow: func(host string) bool { return host == "127.0.0.1" }},
	}
	res := stage.Check(ctx, scfg)
	say(map[string]any{"step": "check", "result": res.Result, "installed": res.Installed, "version": res.Version, "failed_stage": res.FailedStage})
	if res.Result != stage.ResultStaged {
		return 1
	}
	if *mode == "check" {
		return 0
	}
	if *scenario == "tamper-staged" {
		// somebody with the user's rights changes the staged package after it was verified: the apply verifies again from the files and must refuse
		st, err := stage.LoadStaged(*dataDir)
		if err != nil {
			say(map[string]any{"step": "tamper-staged", "result": "failed", "error": err.Error()})
			return 1
		}
		f, err := os.OpenFile(st.PackagePath, os.O_RDWR, 0)
		if err == nil {
			var one [1]byte
			_, _ = f.ReadAt(one[:], 4096)
			one[0] ^= 1
			_, err = f.WriteAt(one[:], 4096)
			f.Close()
		}
		say(map[string]any{"step": "tamper-staged", "changed_a_byte_of": filepath.Base(st.PackagePath), "error": fmt.Sprint(err)})
	}

	platform := &testPlatform{
		Mac:         &apply.Mac{Run: apply.NewRunner(), TeamIDs: pins.MacTeamIDs, Parent: *parent, Self: os.Getpid(), WaitFor: *waitFor, HealthFor: *healthFor},
		startScript: *startScript, skipFirstStart: *scenario == "unhealthy",
	}
	install, err := platform.Install()
	if err != nil {
		say(map[string]any{"step": "install", "result": "failed", "error": err.Error()})
		return 1
	}
	ready := map[string]any{"step": "ready", "install": install, "waiting_for_pid": *parent}
	if st, err := stage.LoadStaged(*dataDir); err == nil && st.Record != nil {
		r := st.Record // what the verifier found: it is a pointer to what to check again at the apply, not proof
		ready["verified"] = map[string]any{"version": r.Version, "tag": r.Tag, "built_from_commit": r.SourceSHA, "package": r.Asset, "package_sha256": r.PackageSHA256, "sums_sha256": r.SumsSHA256}
	}
	say(ready)
	acfg := apply.Config{DataDir: *dataDir, Installed: *installed, Pins: pins, Verifier: verifier, Asset: asset, Platform: platform}
	acfg.AfterSwap = func() {
		say(map[string]any{"step": "swapped", "next_to_the_install": listing(filepath.Dir(install))})
		time.Sleep(*hold)
	}
	ares := apply.Run(ctx, acfg)
	say(map[string]any{"step": "apply", "result": ares.Result, "from": ares.From, "to": ares.To, "failed_stage": ares.FailedStage, "restart_failed": ares.RestartFailed})
	if ares.Result == apply.ResultRelaunched {
		return 0
	}
	return 1
}

// listing names what is in dir (one level), for the operator to see the new and the kept bundle side by side.
func listing(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}
