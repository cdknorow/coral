package ptymanager

import "time"

// HeldAtDialog returns, by session name, since when automated input to that session has been
// held by a request to the user (a prompt, a permission or trust dialog: a hold kind), for the
// health check's notice to the Operator (#4175 step 3; Security's #4176 rule 10). A hold for
// the input box (a draft) or a busy agent is not a dialog and is left out. Only the time is
// given: nothing of the screen or of the held text.
func HeldAtDialog() map[string]time.Time {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	out := map[string]time.Time{}
	for name, held := range gate.held {
		for _, h := range held {
			if h.kind == "" {
				continue
			}
			if since, ok := out[name]; !ok || h.heldAt.Before(since) {
				out[name] = h.heldAt
			}
		}
	}
	return out
}

// HeldAtDraft returns, by session name, since when automated input to that session has been held behind text in its input box
// that isn't Coral's (whyDraft), for the health check's notice (#247): Coral never types over a person's draft, so someone has to
// send or clear it at that terminal. As for HeldAtDialog, only the time is given: nothing of the screen or of the held text.
func HeldAtDraft() map[string]time.Time {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	out := map[string]time.Time{}
	for name, held := range gate.held {
		for _, h := range held {
			if h.why != whyDraft {
				continue
			}
			if since, ok := out[name]; !ok || h.heldAt.Before(since) {
				out[name] = h.heldAt
			}
		}
	}
	return out
}
