package frontend

import (
	"regexp"
	"strings"
	"testing"
)

// Every Settings switch the shell renders has a name and every description it points at exists. The rows built by the includes/toggle_row and
// includes/cloud_switch_row templates are the ones this guards: a typo in a call (a wrong id) would leave a switch unnamed or a description dangling.
func TestShellSettingsSwitchesAreNamedAndDescribed(t *testing.T) {
	tmpl, err := ParseIndex()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, PageData{StaticBase: "/static/b/x/", CoralRoot: "/r"}); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	input := regexp.MustCompile(`<input type="checkbox" id="(settings-[\w-]+)"([^>]*)>`)
	describedby := regexp.MustCompile(`aria-describedby="([^"]*)"`)
	n := 0
	for _, m := range input.FindAllStringSubmatch(html, -1) {
		id := m[1]
		n++
		if !strings.Contains(html, `<label for="`+id+`"`) && !strings.Contains(m[2], "aria-label") && !regexp.MustCompile(`<label[^>]*><input type="checkbox" id="`+id+`"[^>]*>[^<]*[A-Za-z]`).MatchString(html) {
			t.Errorf("switch #%s has no label", id)
		}
		for _, d := range describedby.FindAllStringSubmatch(m[2], -1) {
			for _, target := range strings.Fields(d[1]) {
				if !strings.Contains(html, `id="`+target+`"`) {
					t.Errorf("switch #%s is described by #%s, which does not exist", id, target)
				}
			}
		}
	}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		if strings.HasSuffix(m[1], "-desc") && strings.Count(html, `id="`+m[1]+`"`) != 1 {
			t.Errorf("id %q is used %d times", m[1], strings.Count(html, `id="`+m[1]+`"`))
		}
	}
	if n < 20 {
		t.Errorf("found %d Settings switches, expected at least 20: the census regexp no longer matches the markup", n)
	}
}

// A switch row keeps the markup the dashboard styles: the label names the switch, the description is a sibling span, extra attributes land on the input.
func TestToggleRowTemplateMarkup(t *testing.T) {
	tmpl, err := ParseIndex()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, PageData{StaticBase: "/static/b/x/", CoralRoot: "/r"}); err != nil {
		t.Fatal(err)
	}
	html := regexp.MustCompile(`\s+`).ReplaceAllString(b.String(), " ")
	for _, want := range []string{
		`<div class="settings-row"> <dt class="toggle-dt"> <label for="settings-show-scrollbars">Show scrollbars</label> <span class="toggle-desc" id="settings-show-scrollbars-desc">In the terminal and chat views. Turn off to hide them.</span> </dt> <dd><label class="check-hit"><input type="checkbox" id="settings-show-scrollbars" aria-describedby="settings-show-scrollbars-desc" checked></label></dd> </div>`,
		`<label for="settings-cloud-hub-cross-team" class="settings-cloud-switch-label">Let Coral Hub change cross-team settings</label> <div id="settings-cloud-hub-cross-team-desc" class="settings-hint">`,
		`<input type="checkbox" id="settings-cloud-hub-cross-team" data-cloud="hub-cross-team" aria-describedby="settings-cloud-hub-cross-team-desc">`,
		`<div id="settings-cloud-auto-accept-row" class="settings-cloud-switch-row" data-cloud="auto-accept-row" hidden>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered shell lacks %q", want)
		}
	}
}
