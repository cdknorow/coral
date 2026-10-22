package verify

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The repository moved from cdknorow/coral to subgentic/coral (#5299). The real v1.4.120 fixture was signed in the OLD home, so the tests that use it name that home's pins;
// DefaultPins name the new one, with the repository's id (#5536).

// oldHomePins are the pins of the home the real fixture's release was built in.
func oldHomePins() Pins {
	p := DefaultPins()
	p.Repo, p.RepoID = "cdknorow/coral", "1160696364"
	p.PromoteRepo, p.PromoteRepoID = "cdknorow/coral-promote", "4242"
	return p
}

// withPromoteID is p with a promote repository id, for the tests that run a fake verifier through the whole release (the real pin is empty until that repository exists).
func withPromoteID(p Pins) Pins {
	p.PromoteRepoID = "2222"
	return p
}

func TestDefaultPinsNameTheNewHomeAndItsID(t *testing.T) {
	p := DefaultPins()
	assert.Equal(t, "subgentic/coral", p.Repo)
	assert.Equal(t, "1160696364", p.RepoID, "the id survived the transfer (the certificate carried it before and after)")
	assert.Equal(t, "subgentic/coral-promote", p.PromoteRepo)
	assert.Equal(t, "1429118389", p.PromoteRepoID, "the promote repository's id is pinned: a manifest from a repository of another id does not verify")
}

// The macOS apply installs nothing without a team (see Pins.MacTeamIDs): the one Developer ID team of the app's signature is pinned.
func TestDefaultPinsNameTheMacTeam(t *testing.T) {
	assert.Equal(t, []string{"33UR27T84L"}, DefaultPins().MacTeamIDs)
}

// Nothing verifies without a repository id: a name alone can be taken over by re-creating it.
func TestAnIdentityWithoutARepositoryIDIsRefused(t *testing.T) {
	sums, bundle, v := realFixture(t)
	id := releaseIdentity(oldHomePins(), "v1.4.120")
	id.SourceRepositoryID = ""
	_, err := v.VerifyBundle(bundle, sums, id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repository id")
	// a promote identity with no id (the state before the repository existed) verifies nothing, whatever it is signed with
	noID := DefaultPins()
	noID.PromoteRepoID = ""
	_, err = v.VerifyBundle(bundle, sums, promoteIdentity(noID))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repository id")
	// and with the id pinned, a bundle that the RELEASE workflow signed is still not a promote manifest: the SAN names the other repository
	_, err = v.VerifyBundle(bundle, sums, promoteIdentity(DefaultPins()))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "repository id")
}

// The re-created-name case: someone who re-creates the pinned NAME gets a new numeric id, so the certificate carries an id that is not the pinned one and the bundle is refused,
// although the SAN, the repository URI and every other field are exactly the pinned ones.
func TestARecreatedRepositoryNameFailsOnItsID(t *testing.T) {
	sums, bundle, v := realFixture(t)
	right := oldHomePins()
	_, err := v.VerifyBundle(bundle, sums, releaseIdentity(right, "v1.4.120"))
	require.NoError(t, err, "the real certificate carries id 1160696364")
	recreated := right
	recreated.RepoID = "1160696365" // what the pins say once the real repository's id is another number
	_, err = v.VerifyBundle(bundle, sums, releaseIdentity(recreated, "v1.4.120"))
	require.Error(t, err)
	assert.Regexp(t, `(?i)source ?repository ?id`, err.Error(), "the id is what failed")
}

// The releases cut in the old home are not the new pins' releases: the SAN names the old repository.
func TestTheNewPinsRefuseAReleaseBuiltInTheOldHome(t *testing.T) {
	sums, bundle, v := realFixture(t)
	_, err := v.VerifyBundle(bundle, sums, releaseIdentity(DefaultPins(), "v1.4.120"))
	require.Error(t, err)
}

// Both identities a verification builds carry the pinned repository id (so a change that dropped it from one would not go unseen).
func TestBothIdentitiesCarryTheirRepositoryIDs(t *testing.T) {
	p := withPromoteID(DefaultPins())
	assert.Equal(t, RepoID, releaseIdentity(p, "v1.4.142").SourceRepositoryID)
	assert.Equal(t, "2222", promoteIdentity(p).SourceRepositoryID)
}
