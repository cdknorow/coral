// Package taskcli contains shared task CLI presentation helpers.
package taskcli

import (
	"encoding/json"
	"fmt"
	"io"
)

// PrintBlocked explains unmet dependencies included in a claim response.
func PrintBlocked(w io.Writer, data []byte) {
	var response struct {
		Blocked []struct {
			ID      int64    `json:"id"`
			Title   string   `json:"title"`
			Reasons []string `json:"reasons"`
		} `json:"blocked_tasks"`
	}
	if json.Unmarshal(data, &response) != nil {
		return
	}
	for _, task := range response.Blocked {
		fmt.Fprintf(w, "Task #%d blocked: %s\n", task.ID, task.Title)
		for _, reason := range task.Reasons {
			fmt.Fprintf(w, "  %s\n", reason)
		}
	}
}
