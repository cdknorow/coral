package routes

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
)

// #5793 (QA's #5783 e2e): attach and send-back refused a request that names nobody in the default (log) identity mode, as a task reply did (#5779), because attributedTo gives
// back the empty claim there. A verified actor is the caller in either mode; a name in the body never replaces the actor's (an empty claim only ever takes the actor's), an
// unverified caller is still refused, and mark-landed's digest is accepted bare or with the sha256: prefix of an upload's digest.
func TestQueueActionsTakeTheVerifiedActorInEitherIdentityMode(t *testing.T) {
	for _, enforced := range []bool{true, false} {
		mode := map[bool]string{true: "enforce", false: "log"}[enforced]
		t.Run(mode, func(t *testing.T) {
			bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
			require.NoError(t, err)
			t.Cleanup(func() { bs.Close() })
			h := NewBoardHandler(bs)
			t.Cleanup(h.WaitBackground)
			r := chi.NewRouter()
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if a := req.Header.Get("X-Test-Actor"); strings.HasPrefix(a, "token:") {
						req = withActor(req, Actor{Kind: ActorToken, Name: strings.TrimPrefix(a, "token:")})
					}
					next.ServeHTTP(w, req)
				})
			})
			base := "/api/board/{project}/tasks/{taskID}/"
			r.Post(base+"submit-review", h.SubmitCompletionReview)
			r.Post(base+"review", h.DelegateReview)
			r.Post(base+"approve", h.ApproveReview)
			r.Post(base+"attach", h.AttachPatches)
			r.Post(base+"send-back", h.SendBack)
			r.Post(base+"mark-landed", h.MarkLanded)
			srv := httptest.NewServer(r)
			t.Cleanup(srv.Close)
			was := identityActorEnforced()
			setIdentityActorEnforced(enforced)
			t.Cleanup(func() { setIdentityActorEnforced(was) })
			e := &replyEnv{t: t, srv: srv, h: h, bs: bs}

			for name, role := range map[string]string{"lead": "Orchestrator", "worker": "Developer", "worker2": "Developer", "rev": "QA"} {
				_, err := bs.Subscribe(t.Context(), "mq", name, role, name+"-session", nil, "all")
				require.NoError(t, err)
			}
			// the one status a request with no verified actor and no claim gets: enforce refuses it as unverified, log mode as a request that names nobody
			unverified := map[bool]int{true: http.StatusForbidden, false: http.StatusBadRequest}[enforced]
			as := func(who string) map[string]string { return map[string]string{"X-Test-Actor": "token:" + who} }
			newReviewed := func(title, author string) (id int64, call func(action, body, who string, want int)) {
				tk, err := bs.CreateTask(t.Context(), "mq", title, "", "medium", "lead", author)
				require.NoError(t, err)
				_, err = bs.ClaimTask(t.Context(), "mq", author, tk.ID)
				require.NoError(t, err)
				call = func(action, body, who string, want int) {
					t.Helper()
					var hdr map[string]string
					if who != "" {
						hdr = as(who)
					}
					code, _, b := e.do("POST", "/api/board/mq/tasks/"+itoa(tk.ID)+"/"+action, body, hdr)
					require.Equal(t, want, code, mode+" "+title+" "+action+" as "+who+": "+string(b))
				}
				return tk.ID, call
			}
			patch := func(name, digest string) string {
				return `{"name":"` + name + `","kind":"patch","uri":"coral://artifacts/` + digest + `","digest":"sha256:` + digest + `","revision":"base1"}`
			}
			d1, d2, d3 := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)

			// attach and send-back
			id, call := newReviewed("Fix", "worker")
			call("submit-review", `{"subscriber_id":"worker","reason":"patch","artifacts":[`+patch("fix", d1)+`]}`, "worker", 200)
			call("review", `{"subscriber_id":"lead","reviewers":["rev"]}`, "lead", 200)
			call("attach", `{"artifacts":[`+patch("fix", d2)+`]}`, "", unverified)
			got, err := bs.GetTask(t.Context(), "mq", id)
			require.NoError(t, err)
			require.Len(t, got.Workflow.CompletionReview.Artifacts, 1, mode+": the unverified attach added nothing")
			call("attach", `{"artifacts":[`+patch("fix", d2)+`]}`, "worker", 200) // no subscriber_id: the token's member
			got, err = bs.GetTask(t.Context(), "mq", id)
			require.NoError(t, err)
			require.Len(t, got.Workflow.CompletionReview.Artifacts, 2, mode)
			assert.Equal(t, "worker", got.Workflow.CompletionReview.Artifacts[1].AttachedBy, mode+": the actor's name, not a body name")
			call("approve", `{"subscriber_id":"rev","round":2}`, "rev", 200)
			call("send-back", `{"note":"conflict in a.go"}`, "", unverified)
			call("send-back", `{"note":"conflict in a.go"}`, "worker", http.StatusForbidden) // the actor is the worker, not an orchestrator
			call("send-back", `{"note":"conflict in a.go"}`, "lead", 200)
			got, err = bs.GetTask(t.Context(), "mq", id)
			require.NoError(t, err)
			assert.Equal(t, "pending", got.Status, mode)

			// mark-landed takes the digest bare or with the prefix the upload printed, and still only an approved one
			id2, call2 := newReviewed("Docs", "worker2")
			call2("submit-review", `{"subscriber_id":"worker2","reason":"patch","artifacts":[`+patch("docs", d3)+`]}`, "worker2", 200)
			call2("review", `{"subscriber_id":"lead","reviewers":["rev"]}`, "lead", 200)
			call2("approve", `{"subscriber_id":"rev"}`, "rev", 200)
			call2("mark-landed", `{"digests":["`+strings.Repeat("d", 64)+`"]}`, "lead", http.StatusBadRequest)
			call2("mark-landed", `{"digests":["`+d3+`"]}`, "lead", 200)
			call2("mark-landed", `{"digests":["sha256:`+d3+`"]}`, "lead", 200) // the other form, idempotent
			q, err := bs.MergeQueue(t.Context(), "mq")
			require.NoError(t, err)
			for _, entry := range q {
				assert.NotEqual(t, id2, entry.TaskID, mode+": landed, out of the queue")
			}
		})
	}
}
