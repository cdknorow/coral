// Package eulatext holds the words of the desktop app's native Terms of Service dialogs, apart from cmd/coral-app (whose files all need the webview build tag) so a plain go test pins them (#4360).
package eulatext

// The words of the desktop app's Terms of Service dialog, shared by the macOS and Windows dialogs and checked by a test (#4360 follow-up): what is being
// asked, what each answer does, and that nothing is accepted by pressing Enter. The terminal's own prompt asks for the word "accept" and treats Enter as a
// refusal, so the dialogs make the same promise: the default button is Decline, and Accept is a deliberate choice.
const (
	DialogTitle = "Coral — Terms of Service"
	// DialogIntro is the line above the terms (macOS) or below them (Windows).
	DialogIntro  = "Read the Terms of Service. Choose Accept to continue, or Decline to quit Coral. Nothing is accepted until you choose Accept."
	AcceptLabel  = "Accept"
	DeclineLabel = "Decline"
	// AccessibleName names the scrolling terms for a screen reader.
	AccessibleName = "Terms of Service, scrollable text"
)
