package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeAsAHandler decodes raw the way a board handler does (decodeJSON into a struct).
func decodeAsAHandler(t *testing.T, raw string) (sub, createdBy, title string) {
	t.Helper()
	var b struct {
		SubscriberID string `json:"subscriber_id"`
		CreatedBy    string `json:"created_by"`
		Title        string `json:"title"`
	}
	require.NoError(t, json.NewDecoder(strings.NewReader(raw)).Decode(&b))
	return b.SubscriberID, b.CreatedBy, b.Title
}

// What the guards read (a whole-body parse into a map, exact keys) must be what the handler
// decodes, for every capitalisation of every identity key (#4233).
func TestCanonicalizeIdentityKeysMakesGuardsAndHandlerAgree(t *testing.T) {
	spell := func(k string) []string {
		return []string{k, strings.ToUpper(k), strings.ToUpper(k[:1]) + k[1:], "ſ" + k[1:]}
	}
	for _, k := range identityKeys {
		for _, variant := range spell(k) {
			body := `{"` + variant + `":"Operator","title":"t"}`
			out, _ := canonicalizeIdentityKeys([]byte(body))
			var asMap map[string]string
			require.NoError(t, json.Unmarshal(out, &asMap), "%s", body)
			if canonicalIdentityKey(variant) == "" {
				assert.NotContains(t, asMap, k, "%q is not this field, so it is left alone: %s", variant, out)
				continue
			}
			assert.Equal(t, "Operator", asMap[k], "a guard reading %q sees the claim sent as %q: %s", k, variant, out)
			assert.Equal(t, "t", asMap["title"], "other keys are kept: %s", out)
			if variant != k {
				assert.NotContains(t, asMap, variant, "no other spelling is left: %s", out)
			}
		}
	}
}

func TestCanonicalizeIdentityKeysCases(t *testing.T) {
	cases := []struct {
		name, in string
		changed  bool
		sub, by  string // what a handler decodes from the result
	}{
		{"exact spelling is untouched", `{"subscriber_id":"Dev","title":"t"}`, false, "Dev", ""},
		{"upper case", `{"SUBSCRIBER_ID":"Operator"}`, true, "Operator", ""},
		{"mixed case", `{"Subscriber_ID":"Lead"}`, true, "Lead", ""},
		{"created_by in capitals", `{"CREATED_BY":"Operator","title":"t"}`, true, "", "Operator"},
		{"a later spelling wins", `{"subscriber_id":"Dev","SUBSCRIBER_ID":"Lead"}`, true, "Lead", ""},
		{"an earlier spelling loses", `{"SUBSCRIBER_ID":"Lead","subscriber_id":"Dev"}`, true, "Dev", ""},
		{"exact duplicates keep the decoder's last", `{"subscriber_id":"Lead","subscriber_id":"Dev"}`, false, "Dev", ""},
		{"a null then a spelling", `{"subscriber_id":null,"Subscriber_Id":"Lead"}`, true, "Lead", ""},
		{"escaped key", `{"SUBSCRIBER_ID":"Lead"}`, true, "Lead", ""},
		{"trailing data is dropped", `{"subscriber_id":"Lead"} junk`, true, "Lead", ""},
		{"a second object is dropped", `{"subscriber_id":"Dev"} {"subscriber_id":"Lead"}`, true, "Dev", ""},
		{"leading space", "  \n{\"Subscriber_ID\":\"Lead\"}", true, "Lead", ""},
		{"a non-identity key's case is left alone", `{"Title":"x","TITLE2":"y"}`, false, "", ""},
		{"not an object", `["Lead"]`, false, "", ""},
		{"not JSON", `subscriber_id=Lead`, false, "", ""},
		{"truncated", `{"SUBSCRIBER_ID":"Lead"`, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed := canonicalizeIdentityKeys([]byte(c.in))
			assert.Equal(t, c.changed, changed)
			if !changed {
				assert.Equal(t, c.in, string(out), "an unchanged body is returned as it was")
			}
			if c.sub != "" || c.by != "" {
				sub, by, _ := decodeAsAHandler(t, string(out))
				assert.Equal(t, c.sub, sub)
				assert.Equal(t, c.by, by)
				// And the original, decoded by the handler, said the same: nothing about who the
				// request is from changes meaning, it only becomes visible to the guards.
				osub, oby, _ := decodeAsAHandler(t, c.in)
				assert.Equal(t, osub, sub, "the handler's view is unchanged")
				assert.Equal(t, oby, by)
				var asMap map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(out, &asMap), "a guard's whole-body parse reads the result: %s", out)
			}
		})
	}
}

// The middleware passes a rewritten body on with its new length, and leaves other bodies,
// uploads and other paths alone.
func TestCanonicalIdentityKeysMiddleware(t *testing.T) {
	var gotBody string
	var gotLen int64
	h := CanonicalIdentityKeys(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotLen = string(b), r.ContentLength
	}))
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	do("POST", "/api/board/p/messages", `{"SUBSCRIBER_ID":"Lead","content":"x"} junk`)
	assert.JSONEq(t, `{"content":"x","subscriber_id":"Lead"}`, gotBody)
	assert.EqualValues(t, len(gotBody), gotLen)
	do("PATCH", "/api/agent/tasks/1", `{"Session_ID":"s1"}`)
	assert.JSONEq(t, `{"session_id":"s1"}`, gotBody)
	do("DELETE", "/api/board/p/reminder", `{"Subscriber_ID":"Dev"}`)
	assert.JSONEq(t, `{"subscriber_id":"Dev"}`, gotBody, "any method with a body")
	do("POST", "/api/sessions/launch", `{"SUBSCRIBER_ID":"x"}`)
	assert.Equal(t, `{"SUBSCRIBER_ID":"x"}`, gotBody, "other paths are not touched")
	do("POST", "/api/agent/artifacts", `{"SUBSCRIBER_ID":"x"}`)
	assert.Equal(t, `{"SUBSCRIBER_ID":"x"}`, gotBody, "an artifact upload is the file as sent")
	do("GET", "/api/board/p/messages?Subscriber_ID=Lead", ``)
	assert.Equal(t, "", gotBody)

	// A chunked body over the cap is refused, as the identity layer refuses a declared one.
	big := `{"title":"` + strings.Repeat("a", maxBoardBody) + `"}`
	r := httptest.NewRequest("POST", "/api/board/p/tasks", io.NopCloser(strings.NewReader(big)))
	r.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// Every list of identity fields a guard reads is inside identityKeys, so none can be handed a
// spelling this middleware did not canonicalise.
func TestEveryGuardsIdentityFieldsAreCanonicalised(t *testing.T) {
	in := map[string]bool{}
	for _, k := range identityKeys {
		in[k] = true
	}
	lists := map[string][]string{"identityFields": identityFields, "tunnelActorFields": tunnelActorFields, "tunnelOperatorFields": tunnelOperatorFields,
		"boardClaims": {"subscriber_id", "as", "created_by", "session_name"}}
	for f := range selfIdentityFields {
		lists["selfIdentityFields"] = append(lists["selfIdentityFields"], f)
	}
	for name, l := range lists {
		for _, f := range l {
			assert.True(t, in[f], "%s reads %q, which CanonicalIdentityKeys does not canonicalise", name, f)
		}
	}
}
