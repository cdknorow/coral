//go:build darwin

package agent

import (
	"errors"
	"fmt"
	"os/user"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Directory Services' answers, read for a group the user may be alone in (#5899): the membership, GUID-member and nested-group lines, and the primary-group list.
func TestOnlyUserAndRootReadsDirectoryServicesAnswers(t *testing.T) {
	const gid = "80"
	const three = "GroupMembers: FFFFEEEE-0 FFFFEEEE-F8 DBD3E55E-1\n"
	for name, tc := range map[string]struct {
		read, primary string
		alone         bool
	}{
		"the user and root":                           {"GroupMembership: root alice\n", "alice 80\nbob 20\n", true},
		"the user alone":                              {"GroupMembership: alice\n", "alice 80\n", true},
		"nobody listed (no such key)":                 {"No such key: GroupMembership\n", "alice 80\n", true},
		"as a real Mac lists admin":                   {"No such key: NestedGroups\n" + three + "GroupMembership: root _mbsetupuser alice\n", "alice 80\n", true},
		"Setup Assistant's hidden account is allowed": {"GroupMembership: root _mbsetupuser alice\n", "alice 80\n", true},
		"a second member":                             {"GroupMembership: root alice bob\n", "alice 80\n", false},
		"a second account whose primary group it is":  {"GroupMembership: root alice\n", "alice 80\nbob 80\n", false},
		"another group's primary users do not count":  {"GroupMembership: root alice\n", "alice 80\nbob 20\n_www 70\n", true},
		"a nested group (a managed Mac)":              {"GroupMembership: root alice\nNestedGroups: AAAA-BBBB\n", "alice 80\n", false},
		"more GUID members than accepted names":       {"GroupMembers: A B C D\nGroupMembership: root alice\n", "alice 80\n", false},
		"GUID members continued on the next line":     {"GroupMembers: A B C\n D\nGroupMembership: root alice\n", "alice 80\n", false},
	} {
		assert.Equal(t, tc.alone, onlyUserAndRoot(tc.read, tc.primary, "alice", gid), name)
	}
}

// The system accounts that are not accepted each refuse, as members of the group and as accounts whose primary group it is (#5899 follow-up): only root and _mbsetupuser are let by.
func TestEverySystemAccountButRootAndSetupAssistantRefuses(t *testing.T) {
	const gid = "80"
	for _, who := range []string{"daemon", "nobody", "_www", "_spotlight", "_mbsetupuser2", "Root", "alice2"} {
		assert.False(t, onlyUserAndRoot("GroupMembership: root alice "+who+"\n", "alice 80\n", "alice", gid), who+" as a member")
		assert.False(t, onlyUserAndRoot("GroupMembership: root alice\n", "alice 80\n"+who+" 80\n", "alice", gid), who+" by primary group")
	}
	for _, who := range []string{"root", "_mbsetupuser"} {
		assert.True(t, onlyUserAndRoot("GroupMembership: root alice "+who+"\n", "alice 80\n"+who+" 80\n", "alice", gid), who+" is let by")
	}
}

func TestGroupIsTheUsersAloneAsksDirectoryServicesOnlyForAdminOrThePrimaryGroup(t *testing.T) {
	u, err := user.Current()
	require.NoError(t, err)
	old := runDscl
	t.Cleanup(func() { runDscl = old })
	var asked []string
	runDscl = func(args ...string) (string, error) {
		asked = append(asked, strings.Join(args, " "))
		if args[0] == "-list" {
			return u.Username + " " + u.Gid + "\n", nil
		}
		return "GroupMembership: root " + u.Username + "\n", nil
	}

	// wheel (gid 0) is neither admin nor the user's primary group: no, and Directory Services is not asked
	ok, err := groupIsTheUsersAlone(0)
	require.NoError(t, err)
	assert.False(t, ok, "another group")
	assert.Empty(t, asked)

	// the user's primary group, with only the user in it
	var gid uint32
	_, err = fmt.Sscan(u.Gid, &gid)
	require.NoError(t, err)
	ok, err = groupIsTheUsersAlone(gid)
	require.NoError(t, err)
	assert.True(t, ok, "the primary group, the user alone")

	// a failing Directory Services is a refusal, never a pass
	runDscl = func(...string) (string, error) { return "", errors.New("dscl failed") }
	ok, err = groupIsTheUsersAlone(gid)
	assert.False(t, ok)
	assert.Error(t, err)
}
