package frontend

import (
	"reflect"
	"sort"
	"testing"
)

// The dashboard shell is rendered from PageData and served gzip-compressed and
// shared (the index is cached per build). A secret in a compressed response
// that also reflects attacker-influenced text can leak through its length
// (BREACH), and a cached page would serve one user's value to everyone. So
// PageData may only carry reviewed, non-secret fields: adding one fails this
// test until it is added here after an explicit decision that it is neither a
// secret nor per-user or per-session data (if it must be, the page must stop
// being compressed and cached).
var reviewedPageDataFields = map[string]string{
	"CoralRoot":       "server install path shown in the UI; same for every viewer",
	"EntryMode":       "'' or 'agent' (popout); from the route, not the user",
	"TargetSessionID": "popout's agent id: a UUID taken from the URL, validated, not a credential",
	"TargetServer":    "popout's remote server slug from the URL, validated, not a credential",
	"BootJSON":        "{\"mode\":\"local\"|\"cloud\"}; built by the server from constants",
	"StaticBase":      "static URL prefix for the build (/static/b/<build>/); constant, never from the request",
}

func TestPageDataFieldsAreReviewed(t *testing.T) {
	typ := reflect.TypeOf(PageData{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		got = append(got, name)
		if _, ok := reviewedPageDataFields[name]; !ok {
			t.Errorf("PageData.%s is not reviewed: confirm it is not a secret or per-user/per-session value and add it to reviewedPageDataFields", name)
		}
	}
	sort.Strings(got)
	for name := range reviewedPageDataFields {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("reviewedPageDataFields lists %s, which PageData no longer has", name)
		}
	}
}
