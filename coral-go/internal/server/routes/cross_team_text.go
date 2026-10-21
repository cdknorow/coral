package routes

import (
	"fmt"
	"strings"
)

// The words of cross-team messaging (#5572, operator feedback: "these settings are all over the place"). ONE table, used by the API, the command line (which prints the API's
// message) and the dashboard, so the same refusal reads the same everywhere and always names the screen and the switch.
//
// Two audiences, kept apart on purpose (coral-hub's requirement 5, Security's condition 9):
//   - the person on THIS computer (the owner who can fix it) gets the precise reason and the screen: the texts below that name a team and the Messaging tab;
//   - a sender on ANOTHER Coral gets one answer for every refusal at the receiving door (relayedRefused*), because a precise one would tell it which teams exist
//     and what is switched off there.

// teamSettingsMessaging is where every cross-team switch is: Team settings, the Messaging tab.
const teamSettingsMessaging = "Team settings > Messaging"

// settingsCoralHub is where a Coral is connected to Coral Hub.
const settingsCoralHub = "Settings > Coral Hub"

// What a sender on another Coral is told, whatever the cause: no such team, unlinked, switched off, stale or nobody to tell. The status and the bytes are the same for all.
const (
	relayedRefusedStatus = 404
	relayedRefusedCode   = "not_found"
	relayedRefusedText   = "no such team"
)

// teamList joins quoted team names for a sentence: "a", "a and b".
func teamList(teams []string) string { return strings.Join(teams, " and ") }

// textCrossTeamOff is the refusal of a send from or to a team whose cross-team switch is off. teams are already quoted.
func textCrossTeamOff(teams []string) string {
	return fmt.Sprintf("Cross-team messaging is off on %s. Turn it on in %s.", teamList(teams), teamSettingsMessaging)
}

// textFilesOff is the refusal of a send with files from or to a team that does not share files.
func textFilesOff(teams []string) string {
	return fmt.Sprintf("Sharing files with other teams is off on %s. Turn on Share files with other teams in %s.", teamList(teams), teamSettingsMessaging)
}

// textFilesTurnedOffMidSend is the refusal when the switch went off while files were on their way.
func textFilesTurnedOffMidSend(team, which string) string {
	return fmt.Sprintf("Sharing files with other teams was turned off on %s while the files were being sent; %s was not sent and the message was not sent. Turn it on again in %s.", team, which, teamSettingsMessaging)
}

// textNotLinked is the refusal of a send from a Coral that is not connected to Coral Hub.
const textNotLinked = "This Coral is not connected to Coral Hub. Connect it in " + settingsCoralHub + "."

// textRelayUnsupported is the refusal when the hub has no relay.
const textRelayUnsupported = "Coral Hub does not relay messages (its relay is switched off, or the hub is too old)."

// textHubUnreachable is the refusal when Coral Hub cannot be reached.
const textHubUnreachable = "Could not reach Coral Hub. Retry with the same message_id."

// textNotFoundExplained is what the SENDING person reads when Coral Hub answers not_found (or the old target_refused): every cause looks the same, so the text names where to look
// at both ends and says nothing about which it was.
func textNotFoundExplained(address string) string {
	return fmt.Sprintf("%s was not found: there is no such team, or it is not linked to your team, or it is not taking messages. Check Team links in Coral Hub, and on the receiving team %s.", address, teamSettingsMessaging)
}

// textTargetOffline is the refusal when the other Coral is not connected to the hub.
const textTargetOffline = "The other Coral is offline. Try again later with the same message_id."

// textHubRefused is any other refusal from Coral Hub, by its plain code.
const textHubRefused = "Coral Hub refused the message; retry with the same message_id if it is temporary"

// textForHubRefusal is the sender-facing text for a plain code Coral Hub answered.
func textForHubRefusal(code, address string) string {
	switch {
	case code == "not_found" || strings.HasPrefix(code, "target_refused"):
		return textNotFoundExplained(address)
	case code == "target_offline":
		return textTargetOffline
	}
	return textHubRefused
}

// The owner's status line on the Messaging tab (#5572): the state decides the dot and the button, the text is said as it is. Order of precedence: old, off, stale, waiting, ready.
const (
	statusReady   = "ready"
	statusOff     = "off"
	statusWaiting = "waiting"
	statusStale   = "stale"
	statusOld     = "old"
)

// crossTeamStatus is the state and the words for the owner of a team.
type crossTeamStatus struct {
	State string `json:"state"`
	Text  string `json:"text"`
}

// statusFor names where a team stands. linkable is false for a team made before Coral Hub links existed; linked is how many teams on other Corals can message it; stale says the links
// are paused because Coral Hub has not been heard from (never true before links are kept fresh, #5572 patch 2); heard is when it last was.
func statusFor(linkable, on bool, linked int, stale bool, heard string) crossTeamStatus {
	switch {
	case !linkable:
		return crossTeamStatus{statusOld, "This team was made before Coral Hub links existed, so it cannot use the relay."}
	case !on:
		return crossTeamStatus{statusOff, "Cross-team messaging is off for this team. Turn it on in " + teamSettingsMessaging + "."}
	case stale:
		text := "Links are paused: this Coral has not heard from Coral Hub"
		if heard != "" {
			text += " since " + heard
		}
		return crossTeamStatus{statusStale, text + ". Check its connection in " + settingsCoralHub + "."}
	case linked == 0:
		return crossTeamStatus{statusWaiting, "No team on another Coral is linked to this team yet."}
	case linked == 1:
		return crossTeamStatus{statusReady, "1 team on another Coral can message this team."}
	}
	return crossTeamStatus{statusReady, fmt.Sprintf("%d teams on other Corals can message this team.", linked)}
}
