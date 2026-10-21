// Command routeshapes reads route shapes ("METHOD /path/{}/more", one a line) on stdin and
// prints those the Coral Hub tunnel lets through at some tier (tunnel.ShapeAllowed). It is
// how tools/gen_route_queries.py asks the Go route table, so the query table never lists a
// route the tunnel refuses and the table is never copied into Python (#3942).
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/subgentic/coral/pkg/tunnel"
)

func main() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		method, shape, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if ok && tunnel.ShapeAllowed(method, shape) {
			fmt.Println(method + " " + shape)
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "routeshapes:", err)
		os.Exit(1)
	}
}
