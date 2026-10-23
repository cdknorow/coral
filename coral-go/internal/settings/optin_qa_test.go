package settings

import (
	"sort"
	"strings"
	"testing"
)

// QA on #5147: the opt-in switches (the settings that widen what Coral exposes) turn on for the word true only, in any case: yes, 1, on and a stray
// value leave them off. TestOptInBoolsFailClosed pinned remote_access_enabled alone; removing OptIn from hub_mode, hub_signin_enabled or
// artifact_comments_hub_write failed no test, and they would then read yes, 1, on and 0/no/off as boolean words like any other setting.
func TestEveryOptInBoolFailsClosed(t *testing.T) {
	for _, key := range optInKeys(t) {
		for v, want := range map[string]bool{
			"true": true, "True": true, " TRUE ": true,
			"yes": false, "1": false, "on": false, "enabled": false, "y": false, "t": false, "": false, " ": false, "false": false, "junk": false, "2": false,
		} {
			if got := Bool(map[string]string{key: v}, key); got != want {
				t.Errorf("%s=%q reads %v, want %v", key, v, got, want)
			}
		}
		if Bool(map[string]string{}, key) {
			t.Errorf("%s is on when unset", key)
		}
	}
}

// The set of opt-in switches is pinned: a new setting that widens exposure must be declared OptIn on purpose, and this list changed with it.
func TestTheOptInKeysAreTheKnownOnes(t *testing.T) {
	got := optInKeys(t)
	want := []string{ArtifactCommentsHubWrite, HubMode, HubRelaunchUpdates, "hub_signin_enabled", RemoteAccessEnabled}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the OptIn settings are %v, want %v: a setting that widens exposure must fail closed (only the word true), and be added here", got, want)
	}
}

func optInKeys(t *testing.T) []string {
	t.Helper()
	var keys []string
	for _, s := range All() {
		if s.OptIn {
			if s.Kind != KindBool || s.Default != "false" {
				t.Errorf("%s is OptIn but is not an off-by-default bool", s.Key)
			}
			keys = append(keys, s.Key)
		}
	}
	sort.Strings(keys)
	return keys
}

// #6290: the Hub's owner can relaunch this Coral only after the person here turned it on. Nothing stored is off (a person who never touched it), the word true is on (a person
// who set it on keeps it), and the words that are off elsewhere or are not a word at all are off too.
func TestTheHubRelaunchSwitchIsOffUntilThePersonHereTurnsItOn(t *testing.T) {
	for stored, want := range map[string]bool{"": false, "false": false, "default": false, "on": false, "1": false, "garbage": false, "true": true, " TRUE ": true} {
		if got := Bool(map[string]string{HubRelaunchUpdates: stored}, HubRelaunchUpdates); got != want {
			t.Errorf("stored %q reads %v, want %v", stored, got, want)
		}
	}
	if Bool(map[string]string{}, HubRelaunchUpdates) {
		t.Error("an unset switch is on")
	}
}
