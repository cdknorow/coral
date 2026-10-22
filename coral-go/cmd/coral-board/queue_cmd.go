package main

import (
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// listFlag is a flag that may be given more than once.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

const queueUsageText = `Usage: coral-board queue [--json]

The merge queue (#5689): the approved tasks whose patches have not landed, oldest approval first. Each shows its patches: only the newest
revision of each name that the approving round approved (an older revision stays on the task as history, a revision attached after the
approval is never listed). Rows from submit-review carry no sequence number and sort as the oldest. Whether a patch still applies to the target
branch, and which patches conflict with each other, need git: the person who lands checks them there.`

// cmdQueue prints the merge queue of the board this agent is subscribed to.
func cmdQueue() {
	asJSON := false
	for _, a := range os.Args[2:] {
		switch a {
		case "--json":
			asJSON = true
		default:
			fmt.Fprintln(os.Stderr, queueUsageText)
			exitFn(1)
			return
		}
	}
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role>")
		exitFn(1)
		return
	}
	data, status, err := apiCallRaw("GET", "/"+url.PathEscape(st.Project)+"/queue", nil)
	if err != nil || status != http.StatusOK {
		fmt.Fprintln(os.Stderr, "Could not read the queue: "+clierr.Describe(err, status, data))
		exitFn(1)
		return
	}
	if asJSON {
		fmt.Println(string(data))
		return
	}
	var out struct {
		Queue []struct {
			TaskID     int64    `json:"task_id"`
			Title      string   `json:"title"`
			Author     string   `json:"author"`
			Approvers  []string `json:"approvers"`
			ApprovedAt string   `json:"approved_at"`
			Patches    []struct {
				Name     string `json:"name"`
				Digest   string `json:"digest"`
				Revision string `json:"revision"`
			} `json:"patches"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		fmt.Fprintln(os.Stderr, "Could not read the queue:", err)
		exitFn(1)
		return
	}
	if len(out.Queue) == 0 {
		fmt.Println("The merge queue is empty.")
		return
	}
	for _, e := range out.Queue {
		fmt.Printf("#%d  %s  (by %s, approved by %s at %s)\n", e.TaskID, plainText(e.Title), plainText(e.Author), plainText(strings.Join(e.Approvers, ", ")), plainText(e.ApprovedAt))
		for _, p := range e.Patches {
			fmt.Printf("    %s  digest %s  base %s\n", plainText(p.Name), plainText(p.Digest), plainText(p.Revision))
		}
	}
}

// plainText is s without control characters (ESC and so CSI sequences, the C0 range, DEL, the C1 range): what a task title or a name may
// hold must not move the cursor, recolour or retitle the terminal of the person who reads the queue. --json prints the data as it is.
func plainText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
