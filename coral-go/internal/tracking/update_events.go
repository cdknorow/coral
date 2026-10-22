package tracking

import (
	"strings"
	"sync"
	"sync/atomic"
)

// Update telemetry (#5952). Coral does not start or finish an update itself yet (the updater is switched off on every platform), so nothing here says an update was installed: the two events
// say what Coral can see, that it showed the notice that a newer release exists and that the person opened the release page the notice offers. What happened after that shows as the next
// upgrade event, whose from_version is the version that last ran on the install.
//
// Each is sent once per install, per target release and per surface (the dashboard or the tray), and only while analytics is on: with it off nothing is sent and no state is written. The values are
// closed: a release number by a strict pattern, the surface, the update channel and the method.

// maxUpdateMilestones bounds the update events an install remembers (one milestone key per event, place and release): the page reports through the dashboard route, and a release number is
// cheap to invent, so without a bound the milestone file, loaded and rewritten whole on every call, would grow with whatever a page sent. Real releases are a few a month; the keys past this
// are dropped and nothing is stored for them.
const maxUpdateMilestones = 20

var (
	offeredRelease atomic.Value // string: the release this server's own update check last said is newer than this build
	updateKeysMu   sync.Mutex   // makes "count the update keys, then reserve one" a single step
)

// NoteOfferedRelease is called by the update check when it reports a newer release: the dashboard may report an update event only for that release, never one it names itself.
func NoteOfferedRelease(version string) {
	if version == "" || ValidVersionProp(version) { // "" forgets the offer
		offeredRelease.Store(version)
	}
}

// OfferedRelease is the release NoteOfferedRelease last kept ("" until the update check has offered one).
func OfferedRelease() string {
	v, _ := offeredRelease.Load().(string)
	return v
}

// updateKeyAllowed says whether key may be reserved: it already is one of this install's, or there is room for one more update key. Called with updateKeysMu held.
func updateKeyAllowed(key string) bool {
	if !stateDirReady() {
		return false
	}
	milestoneMu.Lock()
	reached := loadMilestones().Reached
	milestoneMu.Unlock()
	if _, ok := reached[key]; ok {
		return true
	}
	n := 0
	for k := range reached {
		if strings.HasPrefix(k, EventUpdateNoticeShown+"|") || strings.HasPrefix(k, EventUpdateOfferOpened+"|") {
			n++
		}
	}
	return n < maxUpdateMilestones
}

// TrackUpdateNotice records that source (UpdateSourceDashboard or UpdateSourceTray) showed the notice for release toVersion on channel (UpdateChannelApproved or UpdateChannelAgentic).
// An invalid version or source sends nothing; an unknown channel is left out.
func TrackUpdateNotice(toVersion, source, channel string) {
	trackUpdate(EventUpdateNoticeShown, toVersion, source, channel, "")
}

// TrackUpdateOffer records that the person opened the release page for toVersion from source. method is UpdateMethodDownloadLink.
func TrackUpdateOffer(toVersion, source, channel string) {
	trackUpdate(EventUpdateOfferOpened, toVersion, source, channel, UpdateMethodDownloadLink)
}

func trackUpdate(event, toVersion, source, channel, method string) {
	if !ValidVersionProp(toVersion) || (source != UpdateSourceDashboard && source != UpdateSourceTray) {
		return
	}
	if channel != UpdateChannelApproved && channel != UpdateChannelAgentic {
		channel = ""
	}
	if !telemetryEnabled() { // off: nothing is sent, and nothing is remembered
		return
	}
	props := map[string]string{"to_version": toVersion, "source": source}
	if channel != "" {
		props["channel"] = channel
	}
	if method != "" {
		props["method"] = method
	}
	trackOnceKeyed(event+"|"+source+"|"+toVersion, event, props, true)
}

// trackOnceKeyed is TrackOnce for an event that is once per key, not once per install: key is the milestone's name ("event|source|version"), and the event is sent under that key's frozen
// snapshot, retried until the service accepts it, never again after.
func trackOnceKeyed(key, event string, props map[string]string, capped bool) {
	gen := telemetryGen.Load()
	asyncGo(func() {
		var send bool
		var snap pendingMilestone
		if capped {
			updateKeysMu.Lock()
			if updateKeyAllowed(key) {
				send, snap = reserveMilestone(key, posthogKeyPresent() && telemetryValid(gen), props)
			}
			updateKeysMu.Unlock()
		} else {
			send, snap = reserveMilestone(key, posthogKeyPresent() && telemetryValid(gen), props)
		}
		if !send {
			return
		}
		if emitEvent(event, snap.Props, milestoneEventUUID(key, snap.Timestamp), snap.Timestamp, gen) {
			confirmMilestoneSent(key)
			return
		}
		releaseMilestone(key)
	})
}

// updateBrowserProps is what a dashboard report of an update event may carry: the page is the dashboard, whatever it says, and the method is the one the dashboard's link is.
func updateBrowserProps(props map[string]string) (version, channel string, ok bool) {
	version = strings.TrimSpace(props["to_version"])
	if !ValidVersionProp(version) || version != OfferedRelease() { // the release the server's own update check offered, not one the page names
		return "", "", false
	}
	return version, props["channel"], true
}
