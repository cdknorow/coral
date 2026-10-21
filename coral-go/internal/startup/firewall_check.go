package startup

import (
	"log"
	"os"
	"runtime"

	"github.com/subgentic/coral/internal/firewall"
	"github.com/subgentic/coral/internal/safego"
)

// firewallRules reads the machine's firewall rules (a variable so a test can stand in for the registry).
var firewallRules = firewall.ReadRules

// startFirewallCheck, on Windows, reads the firewall rules that name Coral's programs and warns about any inbound allow rule on the
// Public profile or for every port (#97, audit G4A): in the log, and on the dashboard through firewall.Warning. It only reads: a rule
// is changed by the person, in an administrator PowerShell, with the command in the warning.
func startFirewallCheck() {
	defer safego.Recover("startup/firewall_check")
	if runtime.GOOS != "windows" {
		return
	}
	checkFirewall(os.Executable)
}

func checkFirewall(executable func() (string, error)) {
	rules, err := firewallRules()
	if err != nil {
		log.Printf("[firewall] could not read the Windows Firewall rules (%v); not checking them", err)
		return
	}
	exe, _ := executable()
	found := firewall.Assess(rules, exe)
	firewall.Set(found)
	if msg := firewall.Message(found); msg != "" {
		log.Print("[firewall] WARNING: " + msg)
	}
}
