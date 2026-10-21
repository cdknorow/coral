// coral-agents-token turns the "coral-agents" GitHub App key into short-lived installation tokens.
//
//	coral-agents-token --perms push|dispatch [--repo coral|homebrew-coral] [--installation-id N] token
//	coral-agents-token --perms push|dispatch [--repo ...] [--installation-id N] credential get
//
// The key is read from ~/.config/coral-agents/private-key.pem and from nowhere else: no flag, variable or
// input names a key. The token (never the key or the JWT) is printed on standard output only.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	a, err := defaultApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "coral-agents-token:", err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, a))
}

// defaultApp points at the real API and the real key folder. The home folder comes from the user
// database, not from the HOME variable, so the environment cannot redirect the key lookup.
func defaultApp() (*app, error) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		return nil, errors.New("could not find the home folder")
	}
	return &app{
		base: apiBaseURL,
		keys: keyStore{dir: filepath.Join(u.HomeDir, ".config", "coral-agents"), uid: os.Getuid()},
		now:  time.Now,
	}, nil
}

const usage = `usage: coral-agents-token --perms push|dispatch [--repo coral|homebrew-coral] [--installation-id N] token
       coral-agents-token --perms push|dispatch [--repo coral|homebrew-coral] [--installation-id N] credential get|store|erase`

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, a *app) int {
	fs := flag.NewFlagSet("coral-agents-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var req request
	fs.StringVar(&req.Perms, "perms", "", "permission set: push or dispatch")
	fs.StringVar(&req.Repo, "repo", "coral", "repository: coral or homebrew-coral")
	fs.Int64Var(&req.InstallationID, "installation-id", 0, "installation id (default: look it up)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	fail := func(err error) int {
		fmt.Fprintln(stderr, "coral-agents-token:", err)
		return 1
	}
	switch {
	case len(rest) == 1 && rest[0] == "token":
		tok, _, err := a.Token(context.Background(), req)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, tok)
		return 0
	case len(rest) == 2 && rest[0] == "credential":
		return credential(rest[1], req, stdin, stdout, a, fail)
	}
	fmt.Fprintln(stderr, usage)
	return 2
}

// credential speaks git's credential-helper protocol for https://github.com. It answers get for that
// host (and, when git sends a path, for the allowed repository) and stays silent for anything else.
func credential(op string, req request, stdin io.Reader, stdout io.Writer, a *app, fail func(error) int) int {
	switch op {
	case "store", "erase":
		return 0
	case "get":
	default:
		return fail(fmt.Errorf("unknown credential operation %q", op))
	}
	if err := req.validate(); err != nil {
		return fail(err)
	}
	in := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(stdin, 64<<10))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			in[k] = v
		}
	}
	if in["protocol"] != "https" || in["host"] != "github.com" {
		return 0
	}
	if p, ok := in["path"]; ok && p != "" {
		base := strings.TrimSuffix(filepath.Base(p), ".git")
		if base != req.Repo {
			return 0
		}
	}
	tok, until, err := a.Token(context.Background(), req)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "username=x-access-token\npassword=%s\npassword_expiry_utc=%d\n\n", tok, until.Unix())
	return 0
}
