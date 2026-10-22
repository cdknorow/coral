// Package install is the one classifier of what kind of install this Coral is, used by "coral-update status", "check" and "apply" alike (#5280 step 3 B, Codex Test's survey): an install a
// package manager owns is never updated by Coral. Homebrew's cask moves Coral.app to its appdir and leaves a link to it in the Caskroom, and records the cask as installed metadata; either is
// enough here. It reads folders and a few small files and writes nothing, and it has no dependency on the verifier.
package install

import (
	"os"
	"path/filepath"
	"strings"
)

// MacHomebrew is the word for a Homebrew-managed install (the apply's ResultUnsupported reason, the status's install kind).
const MacHomebrew = "homebrew"

// Options say where to look. The zero value looks nowhere.
type Options struct {
	// Prefixes are the Homebrew prefixes (each has a Caskroom folder).
	Prefixes []string
	// Appdirs are the folders Homebrew puts a cask's app in (/Applications, ~/Applications).
	Appdirs []string
}

// MacOptions are the places a Finder-launched Coral looks: the default prefixes whatever the environment says (a Finder-launched process has none of the shell's variables), the one in
// HOMEBREW_PREFIX when it is set, and the two appdirs. A custom prefix a Finder-launched Coral cannot be told about is not seen.
func MacOptions(home, homebrewPrefix string) Options {
	o := Options{Prefixes: []string{"/opt/homebrew", "/usr/local"}, Appdirs: []string{"/Applications"}}
	if homebrewPrefix != "" && filepath.IsAbs(homebrewPrefix) {
		o.Prefixes = append(o.Prefixes, filepath.Clean(homebrewPrefix))
	}
	if home != "" && filepath.IsAbs(home) {
		o.Appdirs = append(o.Appdirs, filepath.Join(home, "Applications"))
	}
	return o
}

// caskNames are the files Homebrew records an installed cask in: the current JSON, the internal JSON and the legacy Ruby caskfile. A missing install receipt is normal and means nothing.
var caskNames = []string{"coral.json", "coral.internal.json", "coral.rb"}

const maxCaskBytes = 256 << 10

// MacManaged returns MacHomebrew when the app at app is one Homebrew installed, "" when it is not (or cannot be told). Two kinds of evidence, either enough:
//   - a Caskroom/coral/<version>/<name>.app link that resolves to this very app (what Homebrew leaves after it moves the app to its appdir);
//   - the cask recorded as installed metadata (Caskroom/coral/.metadata/<version>/<time>/Casks/coral.{json,internal.json,rb}) naming this app's bundle, for an app that sits in an appdir:
//     the metadata says Homebrew installed a bundle of this name, the appdir says this is where it puts it. A Caskroom folder or a receipt alone proves nothing and is not used.
func MacManaged(app string, o Options) string {
	real, err := filepath.EvalSymlinks(app)
	if err != nil {
		return ""
	}
	name := filepath.Base(real)
	inAppdir := false
	for _, d := range o.Appdirs {
		if dr, err := filepath.EvalSymlinks(d); err == nil && filepath.Dir(real) == dr {
			inAppdir = true
		}
	}
	for _, prefix := range o.Prefixes {
		links, _ := filepath.Glob(filepath.Join(prefix, "Caskroom", "coral", "*", name))
		for _, l := range links {
			if target, err := filepath.EvalSymlinks(l); err == nil && target == real {
				return MacHomebrew
			}
		}
		if !inAppdir {
			continue
		}
		for _, cask := range caskNames {
			files, _ := filepath.Glob(filepath.Join(prefix, "Caskroom", "coral", ".metadata", "*", "*", "Casks", cask))
			for _, f := range files {
				if namesApp(f, name) {
					return MacHomebrew
				}
			}
		}
	}
	return ""
}

// namesApp reports whether the cask file names the bundle: its text holds the quoted bundle name, as the JSON's app artifact and the Ruby's app stanza do.
func namesApp(file, name string) bool {
	fi, err := os.Lstat(file)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCaskBytes {
		return false
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `"`+name+`"`)
}
