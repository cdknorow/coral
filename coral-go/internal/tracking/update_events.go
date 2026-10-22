package tracking

import "strings"

// Update telemetry (#5952). Coral does not start or finish an update itself yet (the updater is switched off on every platform), so nothing here says an update was installed: the two events
// say what Coral can see, that it showed the notice that a newer release exists and that the person opened the release page the notice offers. What happened after that shows as the next
// upgrade event, whose from_version is the version that last ran on the install.
//
// Each is sent once per install, per target release and per surface (the dashboard or the tray), and only while analytics is on: with it off nothing is sent and no state is written. The values are
// closed: a release number by a strict pattern, the surface, the update channel and the method.

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
	trackOnceKeyed(event+"|"+source+"|"+toVersion, event, props)
}

// trackOnceKeyed is TrackOnce for an event that is once per key, not once per install: key is the milestone's name ("event|source|version"), and the event is sent under that key's frozen
// snapshot, retried until the service accepts it, never again after.
func trackOnceKeyed(key, event string, props map[string]string) {
	gen := telemetryGen.Load()
	asyncGo(func() {
		send, snap := reserveMilestone(key, posthogKeyPresent() && telemetryValid(gen), props)
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
	if !ValidVersionProp(version) {
		return "", "", false
	}
	return version, props["channel"], true
}
