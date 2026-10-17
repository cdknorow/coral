package routes

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityWords are the words that make a JSON field name look like it names a participant.
var identityWords = map[string]bool{"subscriber": true, "session": true, "sender": true, "creator": true, "created": true, "assign": true,
	"assigned": true, "assignee": true, "reviewer": true, "reviewers": true, "approver": true, "owner": true, "author": true, "actor": true,
	"from": true, "target": true, "as": true, "job": true, "agent": true, "member": true, "members": true, "user": true, "by": true,
	"claimed": true, "released": true, "completed": true, "requester": true, "to": true}

// notIdentityFields are decoded fields that look like an identity but do not say who a request is
// from or for: each with why. A new one needs a line here, or to be added to identityKeys.
var notIdentityFields = map[string]string{
	"agent_type":         "which CLI to launch, not a participant",
	"agents":             "a list of launch specs for a team launch, validated as names by the launcher",
	"reviewers":          "task add --reviewers: names the Operator or orchestrator chose, checked against the board's members when delegated (#4175 rule 13)",
	"to":                 "who a message is for (a mention list): a target, not who it is from",
	"to_address":         "a cross-team address",
	"from_board":         "a board id on a cross-team send; the sender is subscriber_id",
	"target_message_id":  "a message id",
	"target_group_id":    "a group id",
	"target_index":       "an index",
	"target_id":          "a wait target: who or what to wait for, not who is waiting",
	"session":            "a session name addressed by a dashboard action (the Operator's routes)",
	"user":               "a Hub user label set by the tunnel, not by the body",
	"agent":              "a workflow step's agent type",
	"default_agent":      "a workflow's default agent type",
	"blocked_by":         "task ids",
	"completed":          "a flag",
	"created_at":         "a time",
	"current_session_id": "the session a launch replaces, checked by the launcher against the live session",
	"resume_session_id":  "the conversation to resume, not a board participant",
	"from_id":            "a message id",
	"reply_to":           "a message id",
	"job_type":           "what a scheduled job runs",
	"by":                 "a counter label",
}

func TestEveryDecodedIdentityFieldIsCanonicalised(t *testing.T) {
	src := parseRoutesSource(t)
	decoded := map[string]string{} // json field name -> where
	for _, d := range src.decodedBodies() {
		for name := range d.fields {
			decoded[name] = d.fn
		}
	}
	require.Greater(t, len(decoded), 40, "the census must find the handlers' request fields (found %d)", len(decoded))
	require.Contains(t, decoded, "subscriber_id")

	canon := map[string]bool{}
	for _, k := range identityKeys {
		canon[k] = true
	}
	var missing []string
	for name, where := range decoded {
		looks := false
		for _, w := range strings.Split(strings.ToLower(name), "_") {
			if identityWords[w] {
				looks = true
			}
		}
		if !looks || canon[name] {
			continue
		}
		if _, ok := notIdentityFields[name]; ok {
			continue
		}
		missing = append(missing, name+" (decoded in "+where+")")
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "a request field named like a participant is neither canonicalised (identityKeys) nor listed as not an identity (notIdentityFields)")
}
