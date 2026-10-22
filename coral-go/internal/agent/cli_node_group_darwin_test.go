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

// Directory Services' two answers, read for a group the user may be alone in (#5899): the membership line and the primary-group list.
func TestOnlyUserAndRootReadsDirectoryServicesAnswers(t *testing.T) {
	const gid = "80"
	for name, tc := range map[string]struct {
		membership, primary string
		alone               bool
	}{
		"the user and root":                           {"GroupMembership: root alice\n", "alice 80\nbob 20\n", true},
		"Setup Assistant's hidden account is allowed": {"GroupMembership: root _mbsetupuser alice\n", "alice 80\n", true},
		"another underscore account is not":           {"GroupMembership: root _mbsetupuser _www alice\n", "alice 80\n", false},
		"the user alone":                              {"GroupMembership: alice\n", "alice 80\n", true},
		"nobody listed (no such key)":                 {"", "alice 80\n", true},
		"a second member":                             {"GroupMembership: root alice bob\n", "alice 80\n", false},
		"a second account whose primary group it is":  {"GroupMembership: root alice\n", "alice 80\nbob 80\n", false},
		"another group's primary users do not count":  {"GroupMembership: root alice\n", "alice 80\nbob 20\n_www 70\n", true},
	} {
		assert.Equal(t, tc.alone, onlyUserAndRoot(tc.membership, tc.primary, "alice", gid), name)
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
