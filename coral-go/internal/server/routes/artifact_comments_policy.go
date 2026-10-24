package routes

import "github.com/subgentic/coral/internal/store"

// Who may do what with the comments on an artifact (#5176, phase 1): ONE function, called by every route; nothing else in the handlers decides access. The routes derive the
// caller in one place (artifact_comments_caller.go) and ask here. The UI hides what a caller cannot do, but this is the gate.

// Caller kinds.
const (
	commentCallerNone  = ""      // nobody the server can name: refused
	commentCallerOwner = "owner" // the person at this computer (the dashboard, the signed command line, this server's API key), never a request carrying the tunnel's stamp
	commentCallerHub   = "hub"   // a Coral Hub viewer through the authentic tunnel stamp
	commentCallerAgent = "agent" // an agent's session token
)

// Actions on comments.
const (
	commentRead    = "read"
	commentCreate  = "create" // a comment or a reply
	commentEdit    = "edit"
	commentDelete  = "delete"
	commentResolve = "resolve" // resolve or reopen a thread
	commentLock    = "lock"    // the owner's switch: comments on an artifact or a board read only
)

// commentCaller is who is asking: the kind, and the author identity (issuer, subject) a comment they write is stamped with and an ownership check compares. Name is display only.
type commentCaller struct {
	Kind    string
	Issuer  string
	Subject string
	Name    string
	Session string // an agent's session name
	// ViaHub is the Coral Hub's owner (#7052): the owner kind and the owner's own identity, on the requests the tunnel table lists for the owner. What the table does not list (edit, delete, lock) is
	// not offered in the answer to such a caller.
	ViaHub bool
}

// sameAuthor reports whether the caller is the author of c: the kind of identity matches and so do issuer and subject (an empty subject is nobody).
func (c commentCaller) sameAuthor(t *store.ArtifactComment) bool {
	return t != nil && c.Subject != "" && c.Issuer != "" && c.Issuer == t.AuthorIssuer && c.Subject == t.AuthorSubject
}

// commentKindMay is the part of the policy that depends only on WHO is asking and what they want to do, not on the artifact, the comment or the lock: the routes ask it first, before they look
// anything up, so a caller who may never do it learns nothing about which artifacts exist. commentPolicy repeats it, then adds the target and the lock.
func commentKindMay(c commentCaller, action string, hubWrite bool) (bool, string) {
	switch c.Kind {
	case commentCallerOwner, commentCallerHub, commentCallerAgent:
	default:
		return false, "sign in to see comments"
	}
	switch action {
	case commentRead:
		return true, ""
	case commentLock:
		if c.Kind != commentCallerOwner {
			return false, "only the owner of this computer can lock comments"
		}
		return true, ""
	case commentCreate, commentEdit, commentDelete, commentResolve:
	default:
		return false, "unknown action"
	}
	switch c.Kind {
	case commentCallerAgent:
		return false, "agents read comments; they cannot write them yet"
	case commentCallerHub:
		if !hubWrite {
			return false, "commenting from Coral Hub is not turned on yet"
		}
		if c.Subject == "" {
			return false, "Coral Hub did not say who you are, so the comment cannot be attributed" // a write is always someone's: no subject, no write
		}
	}
	return true, ""
}

// commentPolicy decides one action. target is the comment acted on (nil for read, create and lock); locked is the owner's lock on the artifact or its board; hubWrite is whether Coral Hub
// viewers may write at all (off until coral-hub settles a stable subject, #5176). It returns the answer and, when refused, a one-line reason for the 403.
//
//	caller             read   create  edit/delete  resolve  lock
//	owner              yes    yes     own only     yes      yes
//	hub viewer         yes    flag    own, flag    flag     no
//	agent of a board   yes*   no      no           no       no      (* the comments made on a board it is on; the route filters)
//	anyone else        no     no      no           no       no
//
// While locked, every write is refused except the OWNER's delete of their own comment (and the lock and unlock themselves): taking back what you wrote is not commenting. Nobody deletes or
// edits a comment they did not write, the owner included.
func commentPolicy(c commentCaller, action string, target *store.ArtifactComment, locked, hubWrite bool) (bool, string) {
	if ok, why := commentKindMay(c, action, hubWrite); !ok {
		return false, why
	}
	if action == commentRead || action == commentLock {
		return true, ""
	}
	if locked && !(action == commentDelete && c.Kind == commentCallerOwner) {
		return false, "comments on this artifact are locked by the owner"
	}
	switch action {
	case commentCreate, commentResolve:
		return true, ""
	case commentEdit, commentDelete:
		if !c.sameAuthor(target) {
			return false, "you can only " + action + " your own comments"
		}
		return true, ""
	}
	return false, "unknown action"
}
