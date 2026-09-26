package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// runBoardStatus returns the same JSON used by the availability dashboard.
// Reading status never marks messages read, claims work, or assigns tasks.
func runBoardStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	project := fs.String("board", "", "Board name (defaults to current subscription)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: coral-board status [--board NAME]")
	}
	if *project == "" {
		st := loadState()
		if st == nil || st.Project == "" {
			return fmt.Errorf("not subscribed to a board; use --board NAME")
		}
		*project = st.Project
	}
	data, status, err := apiCallRaw(http.MethodGet, "/"+url.PathEscape(*project)+"/status", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("board status failed (HTTP %d): %s", status, data)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, pretty.String())
	return err
}
