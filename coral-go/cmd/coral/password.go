package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/hooks"
)

// The dashboard password from the command line (#3949). Both commands sign their request with
// Coral's command-line token, so only the user Coral runs as, on this computer, can use them.
// The password is read from the terminal (no echo) or, with --stdin, from standard input: never
// from an argument or an environment variable, which other programs of this user can read.

// readNewPassword reads a new password: twice from the terminal, or once from stdin.
func readNewPassword(stdin io.Reader, fromStdin bool, stderr io.Writer) (string, error) {
	if fromStdin {
		b, err := io.ReadAll(io.LimitReader(stdin, auth.MaxPasswordBytes+3))
		if err != nil {
			return "", err
		}
		pw := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
		if pw == "" {
			return "", auth.ErrPasswordEmpty
		}
		return pw, nil
	}
	fd := int(syscall.Stdin)
	if !term.IsTerminal(fd) {
		return "", errors.New("standard input is not a terminal: use --stdin to pass the password on standard input")
	}
	fmt.Fprint(stderr, "New dashboard password: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(stderr, "Again: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("the two passwords differ")
	}
	if len(a) == 0 {
		return "", auth.ErrPasswordEmpty
	}
	return string(a), nil
}

// runSetPassword is `coral set-password`: the first password, on a running Coral.
func runSetPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("set-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fromStdin := fs.Bool("stdin", false, "read the password from standard input (one line) instead of the terminal")
	base := fs.String("url", hooks.CoralBase(), "the running Coral server, on this computer")
	home := fs.String("home", "", "Coral's data folder, if the server was started with --home")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: coral set-password [--stdin] [--url "+defaultCoralURL()+"] [--home DIR]")
		return 2
	}
	*base = runningServerURL(fs, *base, *home)
	c, code := newCloudClient(*base, *home, "set-password", stderr)
	if c == nil {
		return code
	}
	c.label = "coral set-password"
	pw, err := readNewPassword(stdin, *fromStdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "coral set-password: %v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if code := c.call(ctx, http.MethodPost, "/api/auth/password/set", map[string]string{"password": pw}, nil, 30*time.Second, nil); code >= 0 {
		return code
	}
	fmt.Fprintln(stdout, "The dashboard password is set. Browsers sign in with it from now on; the app, the tray and 'coral open' still sign in by themselves.")
	return 0
}

// runResetPassword is `coral reset-password`: a new password (or none, with --clear), every
// browser signed out. With Coral stopped it writes the data folder directly and leaves a marker,
// so the next start signs every browser out.
func runResetPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fromStdin := fs.Bool("stdin", false, "read the new password from standard input (one line) instead of the terminal")
	clearIt := fs.Bool("clear", false, "remove the password instead of setting a new one")
	base := fs.String("url", hooks.CoralBase(), "the running Coral server, on this computer")
	home := fs.String("home", "", "Coral's data folder, if the server was started with --home")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: coral reset-password [--clear | --stdin] [--url "+defaultCoralURL()+"] [--home DIR]")
		return 2
	}
	*base = runningServerURL(fs, *base, *home)
	pw := ""
	if !*clearIt {
		var err error
		if pw, err = readNewPassword(stdin, *fromStdin, stderr); err != nil {
			fmt.Fprintf(stderr, "coral reset-password: %v\n", err)
			return 2
		}
	}
	dir := config.ResolveCoralDir(*home)
	if !coralAnswers(*base) {
		return resetOffline(dir, pw, *clearIt, stdout, stderr)
	}
	c, code := newCloudClient(*base, *home, "reset-password", stderr)
	if c == nil {
		return code
	}
	c.label = "coral reset-password"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var out struct {
		SignedOut int `json:"signed_out"`
	}
	body := map[string]any{"password": pw}
	if *clearIt {
		body = map[string]any{"clear": true}
	}
	if code := c.call(ctx, http.MethodPost, "/api/auth/password/reset", body, nil, 30*time.Second, &out); code >= 0 {
		return code
	}
	what := "The dashboard password was reset"
	if *clearIt {
		what = "The dashboard password was removed"
	}
	fmt.Fprintf(stdout, "%s; %d browser session(s) were signed out.\n", what, out.SignedOut)
	return 0
}

// coralAnswers reports whether a Coral answers its health check at base.
func coralAnswers(base string) bool {
	if !loopbackURL(strings.TrimRight(base, "/")) {
		return true // not ours to write: let the signed client refuse it
	}
	cl := &http.Client{Timeout: 2 * time.Second}
	resp, err := cl.Get(strings.TrimRight(base, "/") + "/api/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// resetOffline changes the password file of a stopped Coral and marks every session for ending.
func resetOffline(dir, pw string, clearIt bool, stdout, stderr io.Writer) int {
	if _, err := os.Stat(filepath.Join(dir, "api_key")); err != nil {
		fmt.Fprintf(stderr, "coral reset-password: Coral is not running, and %s is not Coral's data folder (no api_key): pass --home, or start Coral\n", dir)
		return 2
	}
	if clearIt {
		if err := auth.RemovePasswordHash(dir); err != nil {
			fmt.Fprintf(stderr, "coral reset-password: %v\n", err)
			return 1
		}
	} else {
		phc, err := auth.HashPassword(pw)
		if err != nil {
			fmt.Fprintf(stderr, "coral reset-password: %v\n", err)
			return 2
		}
		if err := auth.WritePasswordHash(dir, phc); err != nil {
			fmt.Fprintf(stderr, "coral reset-password: %v\n", err)
			return 1
		}
	}
	if err := os.WriteFile(filepath.Join(dir, auth.SessionsRevokedFile), []byte("reset\n"), 0o600); err != nil {
		fmt.Fprintf(stderr, "coral reset-password: the password was changed, but every browser couldn't be marked for sign-out: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Coral is not running: the password was changed in its data folder, and every browser will be signed out when Coral starts.")
	return 0
}

// runSetupCode is `coral setup-code`: the one-time code that creating the password from another
// computer asks for, while no password is set.
func runSetupCode(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup-code", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("url", hooks.CoralBase(), "the running Coral server, on this computer")
	home := fs.String("home", "", "Coral's data folder, if the server was started with --home")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: coral setup-code [--url "+defaultCoralURL()+"] [--home DIR]")
		return 2
	}
	*base = runningServerURL(fs, *base, *home)
	c, code := newCloudClient(*base, *home, "setup-code", stderr)
	if c == nil {
		return code
	}
	c.label = "coral setup-code"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var out struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in"`
	}
	if code := c.call(ctx, http.MethodGet, "/api/auth/setup-code", nil, nil, 10*time.Second, &out); code >= 0 {
		return code
	}
	fmt.Fprintf(stdout, "%s\n", out.Code)
	fmt.Fprintf(stderr, "One use, valid %d more minutes, until a password is set. Do not share it.\n", (out.ExpiresIn+59)/60)
	return 0
}
