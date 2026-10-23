// strip-static writes the build-time overlay that ships the dashboard minified (#6208): see internal/stripstatic. Run from the coral-go folder:
//
//	go run ./tools/strip-static                  makes a fresh owner-only folder, prints the overlay.json path (the only thing on stdout)
//	go run ./tools/strip-static -out <dir>       the same into <dir>, which must be a new, empty folder named coral-static-* (Windows steps make it owner-only with icacls first)
//	go run ./tools/strip-static -check <overlay> validates an overlay (what a release runs on the one it used)
//	go run ./tools/strip-static -clean <overlay> deletes that overlay's folder by its exact path
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/stripstatic"
)

func main() {
	static := flag.String("static", filepath.FromSlash("pkg/frontend/static"), "the static tree (source)")
	check := flag.String("check", "", "validate this overlay and exit")
	out := flag.String("out", "", "write the overlay into this existing empty folder named coral-static-* instead of a temp folder")
	clean := flag.String("clean", "", "delete this overlay's folder and exit")
	flag.Parse()
	switch {
	case *check != "":
		if err := stripstatic.Validate(*check, *static); err != nil {
			fail(err)
		}
		fmt.Fprintln(os.Stderr, "overlay ok:", *check)
	case *clean != "":
		dir := filepath.Dir(*clean)
		if filepath.Base(*clean) != stripstatic.File || !strings.HasPrefix(filepath.Base(dir), "coral-static-") {
			fail(fmt.Errorf("refusing to delete %s: not an overlay folder this tool made", dir))
		}
		if err := os.RemoveAll(dir); err != nil {
			fail(err)
		}
	default:
		dir := *out
		if dir == "" {
			var err error
			dir, err = os.MkdirTemp("", "coral-static-") // fresh and owner-only (0700), never a fixed path
			if err != nil {
				fail(err)
			}
		} else if err := stripstatic.CheckOutDir(dir); err != nil {
			fail(err)
		}
		res, err := stripstatic.Generate(*static, dir)
		if err != nil {
			os.RemoveAll(dir)
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "%d files stripped, %d bytes to %d (%d JS and CSS files unchanged)\n", res.Files, res.SourceBytes, res.StrippedBytes, res.Unchanged)
		fmt.Println(res.Overlay)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "strip-static:", err)
	os.Exit(1)
}
