package main

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/go-github/v88/github"
)

const (
	appID      int64 = 5405319     // the "coral-agents" GitHub App
	appOwner         = "subgentic" // the organization the App is installed on: a token for a repository of any other account is refused
	apiBaseURL       = "https://api.github.com"

	jwtLifetime = 9 * time.Minute  // from now: GitHub allows 10 minutes from the issue time
	jwtBackdate = 60 * time.Second // the issue time, in the past, for clock drift

	callTimeout = 30 * time.Second
)

// permSets are the only permission sets a caller can ask for. A caller names a set, never a permission.
var permSets = map[string]map[string]string{
	"push":     {"contents": "write", "pull_requests": "write"},
	"dispatch": {"actions": "write"},
}

var allowedRepos = map[string]bool{"coral": true, "homebrew-coral": true}

// request is what one run asks for.
type request struct {
	InstallationID int64 // 0: look it up with GET /app/installations
	Repo           string
	Perms          string // a key of permSets
}

func (r request) validate() error {
	if _, ok := permSets[r.Perms]; !ok {
		return fmt.Errorf("unknown permission set %q: use one of %s", r.Perms, strings.Join(setNames(), ", "))
	}
	if !allowedRepos[r.Repo] {
		return fmt.Errorf("repository %q is not allowed: use coral or homebrew-coral", r.Repo)
	}
	if r.InstallationID < 0 {
		return errors.New("the installation id must be a positive number")
	}
	return nil
}

func setNames() []string {
	var out []string
	for n := range permSets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// installationPermissions turns a permission set into the API's type; a set with a name it does not know
// is a programming error, so it panics rather than ask GitHub for something unplanned.
func installationPermissions(set map[string]string) *github.InstallationPermissions {
	p := &github.InstallationPermissions{}
	for name, level := range set {
		l := level
		switch name {
		case "contents":
			p.Contents = &l
		case "pull_requests":
			p.PullRequests = &l
		case "actions":
			p.Actions = &l
		default:
			panic("permission without a mapping: " + name)
		}
	}
	return p
}

// appSigner replaces the claims the library would sign with Coral's: issued 60 seconds in the past,
// valid 9 minutes from now, issuer the App id. The library's own claims are shorter and are not ours.
type appSigner struct {
	key *rsa.PrivateKey
	now func() time.Time
}

func (s appSigner) Sign(jwt.Claims) (string, error) {
	now := s.now()
	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(appID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtBackdate).Truncate(time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime).Truncate(time.Second)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(s.key)
}

// app is the helper with everything a test replaces: the API base, the key folder and the clock.
type app struct {
	base string
	keys keyStore
	now  func() time.Time
}

// httpClient never follows a redirect: the JWT and the token go to the API host and nowhere else.
func httpClient() *http.Client {
	return &http.Client{
		Timeout:       callTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (a *app) appsTransport() (*ghinstallation.AppsTransport, error) {
	pem, err := a.keys.readKey()
	if err != nil {
		return nil, err
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pem)
	if err != nil {
		return nil, errors.New("the key file is not a PEM RSA private key")
	}
	atr, err := ghinstallation.NewAppsTransportWithOptions(http.DefaultTransport, appID,
		ghinstallation.WithSigner(appSigner{key: key, now: a.now}))
	if err != nil {
		return nil, errors.New("could not set up the App transport")
	}
	atr.BaseURL = a.base
	atr.Client = httpClient()
	return atr, nil
}

// lookupInstallation asks GET /app/installations and accepts exactly one installation.
func (a *app) lookupInstallation(ctx context.Context, atr *ghinstallation.AppsTransport) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.base, "/")+"/app/installations?per_page=100", nil)
	if err != nil {
		return 0, errors.New("could not build the installation lookup")
	}
	resp, err := atr.RoundTrip(req)
	if err != nil {
		return 0, errors.New("the installation lookup failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the installation lookup answered %s", resp.Status)
	}
	var list []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return 0, errors.New("the installation lookup answered something unreadable")
	}
	// Only an installation on the organization counts (#5528 review): the App installed on another account by mistake must not be the one a token is minted for.
	own := list[:0]
	for _, in := range list {
		if strings.EqualFold(in.Account.Login, appOwner) {
			own = append(own, in)
		}
	}
	list = own
	switch len(list) {
	case 1:
		if list[0].ID <= 0 {
			return 0, errors.New("the installation lookup returned no id")
		}
		return list[0].ID, nil
	case 0:
		return 0, errors.New("the App has no installation: install it first, or pass --installation-id")
	default:
		return 0, fmt.Errorf("the App has %d installations: pass --installation-id", len(list))
	}
}

// fetch asks GitHub for a token narrowed to the request and checks that the answer is as narrow.
func (a *app) fetch(ctx context.Context, req request) (cacheEntry, error) {
	atr, err := a.appsTransport()
	if err != nil {
		return cacheEntry{}, err
	}
	id := req.InstallationID
	if id == 0 {
		if id, err = a.lookupInstallation(ctx, atr); err != nil {
			return cacheEntry{}, err
		}
	}
	itr := ghinstallation.NewFromAppsTransport(atr, id)
	itr.BaseURL = a.base
	itr.Client = httpClient()
	itr.InstallationTokenOptions = &github.InstallationTokenOptions{
		Repositories: []string{req.Repo},
		Permissions:  installationPermissions(permSets[req.Perms]),
	}
	tok, err := itr.Token(ctx)
	if err != nil {
		return cacheEntry{}, fmt.Errorf("GitHub refused the token request for installation %d: %s", id, statusOf(err))
	}
	if tok == "" {
		return cacheEntry{}, errors.New("GitHub answered without a token")
	}
	if err := checkNarrow(itr, req); err != nil {
		return cacheEntry{}, err
	}
	expires, _, err := itr.Expiry()
	if err != nil || expires.IsZero() {
		return cacheEntry{}, errors.New("GitHub answered without an expiry")
	}
	return cacheEntry{Token: tok, ExpiresAt: expires, FetchedAt: a.now(), InstallationID: id, Repo: req.Repo, Perms: req.Perms}, nil
}

// statusOf names the HTTP status of a failed token request and nothing else about it.
func statusOf(err error) string {
	var he *ghinstallation.HTTPError
	if errors.As(err, &he) && he.Response != nil {
		return he.Response.Status
	}
	return "no usable answer"
}

// checkNarrow refuses a token that is wider than the one asked for. GitHub answers with what it granted.
func checkNarrow(itr *ghinstallation.Transport, req request) error {
	got, err := itr.Permissions()
	if err != nil {
		return errors.New("GitHub did not say what the token may do: refused")
	}
	raw, _ := json.Marshal(got)
	var gotMap map[string]string
	if json.Unmarshal(raw, &gotMap) != nil {
		return errors.New("GitHub's permissions were unreadable: refused")
	}
	want := map[string]string{}
	for k, v := range permSets[req.Perms] {
		want[k] = v
	}
	if gotMap["metadata"] == "read" { // GitHub adds metadata:read to every installation token
		want["metadata"] = "read"
	}
	if len(gotMap) != len(want) {
		return fmt.Errorf("the token's permissions are not the %s set: refused", req.Perms)
	}
	for k, v := range want {
		if gotMap[k] != v {
			return fmt.Errorf("the token's permissions are not the %s set: refused", req.Perms)
		}
	}
	repos, err := itr.Repositories()
	if err != nil || len(repos) != 1 || repos[0].GetName() != req.Repo || !strings.EqualFold(repos[0].GetFullName(), appOwner+"/"+req.Repo) {
		return fmt.Errorf("the token is not limited to the repository %s: refused", req.Repo)
	}
	return nil
}

// Token returns a token for the request: from the cache while it is fresh, else from GitHub.
func (a *app) Token(ctx context.Context, req request) (string, time.Time, error) {
	if err := req.validate(); err != nil {
		return "", time.Time{}, err
	}
	if err := checkDir(a.keys.dir, a.keys.uid); err != nil {
		return "", time.Time{}, err
	}
	if e, ok := a.readCache(req); ok {
		return e.Token, e.usableUntil(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	e, err := a.fetch(ctx, req)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := a.writeCache(req, e); err != nil {
		return "", time.Time{}, err
	}
	return e.Token, e.usableUntil(), nil
}
