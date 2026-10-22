//go:build darwin

package agent

import (
	"context"
	"fmt"
	"os/exec"
	"os/user"
	"strings"
	"time"
)

// runDscl asks Directory Services (a variable so a test can answer for it).
var runDscl = func(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/dscl", append([]string{"."}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	return string(out), err
}

// groupIsTheUsersAlone says whether a group write bit on one of the user's own folders grants nobody else anything: the group is admin or the user's primary group, and Directory
// Services lists no one in it but the user and root, by membership (GroupMembership) and by primary group (every local account whose PrimaryGroupID is the group's: on a Mac every user's
// is staff, so staff is never the user's alone on a Mac with a second account). Any error, any other member or any other group is a no.
func groupIsTheUsersAlone(gid uint32) (bool, error) {
	u, err := user.Current()
	if err != nil {
		return false, err
	}
	g, err := user.LookupGroupId(fmt.Sprint(gid))
	if err != nil {
		return false, err
	}
	if g.Name != "admin" && fmt.Sprint(gid) != u.Gid {
		return false, nil
	}
	members, err := runDscl("-read", "/Groups/"+g.Name, "GroupMembership", "GroupMembers", "NestedGroups")
	if err != nil && !strings.Contains(members, "No such key") {
		return false, err
	}
	users, err := runDscl("-list", "/Users", "PrimaryGroupID")
	if err != nil {
		return false, err
	}
	return onlyUserAndRoot(members, users, u.Username, fmt.Sprint(gid)), nil
}

// apartFromTheUser are the accounts that may be in the group besides the user: root, and _mbsetupuser, the hidden account macOS's Setup Assistant makes (no login, no home) that sits in admin on
// a Mac set up the usual way: without it admin is never the user's alone on a normal Mac. Every other account, system or not, is someone else.
var apartFromTheUser = map[string]bool{"root": true, "_mbsetupuser": true}

// onlyUserAndRoot reads dscl's two answers: the group's "GroupMembership" (names), "GroupMembers" (one GUID each) and "NestedGroups" lines, and the "name gid" lines of the user list. The
// group is the user's alone when its names are the user and the accepted accounts, it has no nested group (a managed Mac can put a directory group inside admin, whose members the names
// do not show), it has no more GUID members than names listed (a member that appears only as a GUID is someone the names do not show), and no other account has it as its primary group.
func onlyUserAndRoot(read, primary, me, gid string) bool {
	attrs := dsclAttributes(read)
	for _, m := range attrs["GroupMembership"] {
		if m != me && !apartFromTheUser[m] {
			return false
		}
	}
	if len(attrs["NestedGroups"]) > 0 {
		return false
	}
	if len(attrs["GroupMembers"]) > len(attrs["GroupMembership"]) { // every member has a name on a normal record: a GUID with no name is someone the names do not show
		return false
	}
	for _, line := range strings.Split(primary, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == gid && f[0] != me && !apartFromTheUser[f[0]] {
			return false
		}
	}
	return true
}

// dsclAttributes splits a dscl -read answer into attribute -> values: "Name: a b" starts one, a line that begins with white space continues the one before, and "No such key: X" is no attribute.
func dsclAttributes(out string) map[string][]string {
	attrs := map[string][]string{}
	last := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if last != "" {
				attrs[last] = append(attrs[last], strings.Fields(line)...)
			}
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, "No such key") {
			last = ""
			continue
		}
		last = strings.TrimSpace(name)
		attrs[last] = append(attrs[last], strings.Fields(rest)...)
	}
	return attrs
}
