package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func cmdUI(args []string) {
	if err := runUI(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func runUI(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println(`Usage: coral-agent ui <publish|list|events|remove> [flags]
  publish --id ID --file FILE [--title TITLE]  Publish HTML, SVG, PNG, JPEG, GIF, or WebP
  list                                        List your panels
  events --id ID [--after N]                   Read up to 100 interaction events
  remove --id ID                              Delete panel and its events

Agent workflow: delegate panel creation, validation, and publishing to a subagent
using the originating Coral session. The parent hands off without reviewing,
retesting, publishing, or sending progress/completion text. The panel is the
response; report only blockers unless the user requests otherwise.
See agent_docs/agent-ui.md for the full handoff rule.`)
		return nil
	}
	sub := args[0]
	if sub != "publish" && sub != "list" && sub != "events" && sub != "remove" {
		return fmt.Errorf("unknown ui command %q", sub)
	}
	f := flag.NewFlagSet("ui "+sub, flag.ContinueOnError)
	id := f.String("id", "", "Stable panel ID")
	file := f.String("file", "", "Local file")
	title := f.String("title", "", "Panel title")
	after := f.Int64("after", 0, "Event cursor")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if sub != "list" && *id == "" {
		return fmt.Errorf("--id is required")
	}
	path := "/ui"
	if sub != "list" {
		path += "/" + url.PathEscape(*id)
	}
	method := "GET"
	var body any
	switch sub {
	case "publish":
		content, err := uiFileHTML(*file)
		if err != nil {
			return err
		}
		if *title == "" {
			*title = *id
		}
		body = map[string]string{"title": *title, "html": content}
		method = "PUT"
	case "events":
		path += "/events"
	case "remove":
		method = "DELETE"
	}
	path += "?session_id=" + url.QueryEscape(resolveSessionID())
	if sub == "events" {
		if *after < 0 {
			return fmt.Errorf("--after must be nonnegative")
		}
		path += fmt.Sprintf("&after=%d", *after)
	}
	data, status, err := apiCallRaw(method, path, body)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("Coral returned %d: %s", status, data)
	}
	fmt.Println(string(data))
	return nil
}
func uiFileHTML(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("--file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 2<<20 {
		return "", fmt.Errorf("file exceeds 2 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".html", ".htm", ".svg":
		return string(data), nil
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		mime := http.DetectContentType(data)
		if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" && mime != "image/webp" {
			return "", fmt.Errorf("file is not a supported raster image")
		}
		result := `<style>body{margin:0;background:#202020}img{display:block;max-width:100%;height:auto;margin:auto}</style><img alt="` + html.EscapeString(filepath.Base(path)) + `" src="data:` + mime + `;base64,` + base64.StdEncoding.EncodeToString(data) + `">`
		if len(result) > 2<<20 {
			return "", fmt.Errorf("embedded image exceeds 2 MiB")
		}
		return result, nil
	default:
		return "", fmt.Errorf("use HTML, SVG, PNG, JPEG, GIF, or WebP")
	}
}
