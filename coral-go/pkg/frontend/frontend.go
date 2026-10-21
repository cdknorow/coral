// Package frontend exports Coral's web UI assets so other binaries (the Cloud
// Hub) can serve the same frontend as the Coral server. Paths inside the
// filesystems keep their "static/" and "templates/" prefixes.
package frontend

import (
	"embed"
	"html/template"
	"path"
)

// Static holds the files served under /static/ (JavaScript modules, CSS, vendor
// bundles, icons), rooted at "static/".
//
//go:embed all:static
var Static embed.FS

// Templates holds the Go html/template sources of the single-page app shell,
// rooted at "templates/".
//
//go:embed all:templates
var Templates embed.FS

// PageData is what the app shell template needs. The Coral server and the Cloud
// Hub both render it.
type PageData struct {
	CoralRoot       string
	EntryMode       string
	TargetSessionID string
	// TargetServer is the registered remote the popout agent lives on; empty
	// means the hub itself (legacy /agent/{id}).
	TargetServer string
	// BootJSON is the content of the <script type="application/json" id="coral-boot">
	// element: a JSON object the page reads at startup ({"mode":"cloud"} on the
	// Cloud Hub). It is data, not code, so a CSP without 'unsafe-inline' allows it.
	// Empty means a standalone Coral: {"mode":"local"}. Must be valid JSON built
	// by the server from trusted values (it is emitted unescaped).
	BootJSON template.JS
	// StaticBase prefixes every static URL in the shell templates (index and
	// the cloud sign-in/enroll pages); empty means "/static/". It is set by the
	// embedder, is trusted and constant for a build, always ends in "/", has no
	// query or fragment, and is NEVER derived from the request: the rendered page
	// is shared by everyone and may be cached (no per-user or per-session data in
	// PageData, ever). The standalone server uses /static/b/<build>/, deliberately
	// distinct from the Cloud Hub's /static/v/<hash>/, so the two never collide.
	StaticBase string
	// ModulePreloads are the startup modules (paths relative to StaticBase), rendered as
	// <link rel="modulepreload"> so the browser fetches the whole import graph at once instead of
	// one level at a time (#3900). StartupModules() computes it from the embedded files: it is
	// the same for every viewer and every request, never from input. Empty (the Cloud Hub's own
	// pages, a test) means no hints.
	ModulePreloads []string
	// StylesheetBundle, when set, is the path (relative to StaticBase) of the one stylesheet that replaces the shell's 24 links
	// (BundleStylesheet, #4228); the server sets it only together with the bundled StaticBase. Empty means the individual links.
	StylesheetBundle string
}

// IndexTemplateFiles lists the template files that make up the app shell.
var IndexTemplateFiles = []string{
	"templates/index.html",
	"templates/includes/sidebar.html",
	"templates/includes/right_dock.html",
	"templates/includes/modals.html",
	"templates/includes/views/live_session.html",
	"templates/includes/views/history_session.html",
	"templates/includes/views/message_board.html",
	"templates/includes/views/workflows.html",
	"templates/includes/views/docs.html",
	"templates/includes/views/servers.html",
	"templates/includes/views/inbox.html",
	"templates/includes/views/cost_dashboard.html",
}

// ParseIndex parses the app shell templates.
func ParseIndex() (*template.Template, error) {
	return template.New(path.Base(IndexTemplateFiles[0])).Funcs(templateFuncs).ParseFS(Templates, IndexTemplateFiles...)
}

// ToggleRow is the argument of the includes/toggle_row template. The label and description are trusted literals from the template files;
// the funcs below are not reachable from page data.
type ToggleRow struct {
	ID    string
	Label template.HTML
	Desc  template.HTML
	Attrs template.HTMLAttr
}

// CloudSwitchRow is the argument of the includes/cloud_switch_row template; same trust as ToggleRow.
type CloudSwitchRow struct {
	RowID, InputID, DescID string
	RowAttrs, InputAttrs   template.HTMLAttr
	Label, Desc            template.HTML
}

var templateFuncs = template.FuncMap{
	// cloudSwitch builds a CloudSwitchRow: rowID, rowAttrs, inputID, label, descID, desc, inputAttrs.
	"cloudSwitch": func(rowID, rowAttrs, inputID, label, descID, desc, inputAttrs string) CloudSwitchRow {
		sp := func(a string) template.HTMLAttr {
			if a == "" {
				return ""
			}
			return template.HTMLAttr(" " + a)
		}
		return CloudSwitchRow{RowID: rowID, RowAttrs: sp(rowAttrs), InputID: inputID, Label: template.HTML(label), DescID: descID, Desc: template.HTML(desc), InputAttrs: sp(inputAttrs)}
	},
	// toggle builds a ToggleRow: toggle "settings-x" "Label" "Description" `data-on-change="m-x"` `checked`.
	"toggle": func(id, label, desc string, attrs ...string) ToggleRow {
		a := ""
		for _, x := range attrs {
			a += " " + x
		}
		return ToggleRow{ID: id, Label: template.HTML(label), Desc: template.HTML(desc), Attrs: template.HTMLAttr(a)}
	},
}
