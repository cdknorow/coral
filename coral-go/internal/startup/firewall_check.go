package startup

import (
	"context"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/subgentic/coral/internal/firewall"
	"github.com/subgentic/coral/internal/safego"
)

// firewallRules reads the machine's firewall rules (a variable so a test can stand in for the registry).
var firewallRules = firewall.ReadRules

// firewallRecheck are the waits before the later looks: the first listen on a network address makes Windows ask whether to allow
// the program, and the rule the answer makes does not exist until the person has answered, so a single look just after the
// listeners open would miss it until the next start (#97 L1).
var firewallRecheck = []time.Duration{time.Minute, 10 * time.Minute, time.Hour}

// startFirewallCheck, on Windows, reads the firewall rules that name Coral's programs and warns about any inbound allow rule on the
// Public profile (#97, audit G4A): in the log, and on the dashboard through firewall.Warning. It looks once now and again after a minute,
// ten minutes and an hour, logging only when the finding changes, and it stops when ctx (the server's) ends, so a shutdown leaves no
// goroutine behind. It only reads: a rule is changed by the person, in an administrator PowerShell, with the command in the warning.
func startFirewallCheck(ctx context.Context) {
	defer safego.Recover("startup/firewall_check")
	if runtime.GOOS != "windows" {
		return
	}
	runFirewallChecks(ctx, os.Executable, firewallRecheck)
}

// runFirewallChecks looks now and after each wait, until ctx ends.
func runFirewallChecks(ctx context.Context, executable func() (string, error), waits []time.Duration) {
	last := checkFirewall(executable, "")
	for _, wait := range waits {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		last = checkFirewall(executable, last)
	}
}

// checkFirewall records what the rules say now and logs the warning unless it is the one logged last time; it returns the message
// that is now in effect.
func checkFirewall(executable func() (string, error), last string) string {
	rules, err := firewallRules()
	if err != nil {
		log.Printf("[firewall] could not read the Windows Firewall rules (%v); not checking them", err)
		return last
	}
	exe, _ := executable()
	found := firewall.Assess(rules, exe)
	firewall.Set(found)
	msg := firewall.Message(found)
	if msg != "" && msg != last {
		log.Print("[firewall] WARNING: " + msg)
	}
	return msg
}
