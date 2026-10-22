//go:build linux

package secfile

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acl(entries ...[3]uint32) []byte {
	b := make([]byte, 4, 4+8*len(entries))
	binary.LittleEndian.PutUint32(b, 2)
	for _, e := range entries {
		x := make([]byte, 8)
		binary.LittleEndian.PutUint16(x, uint16(e[0]))
		binary.LittleEndian.PutUint16(x[2:], uint16(e[1]))
		binary.LittleEndian.PutUint32(x[4:], e[2])
		b = append(b, x...)
	}
	return b
}

// The POSIX access list (system.posix_acl_access): a named user or group with write, kept by the mask, is a writer; the user, root, entries without write, and a mask that takes the
// write away are not; a list that is not understood is an error.
func TestParsePOSIXACL(t *testing.T) {
	const (
		userObj, user, groupObj, group, mask, other = 1, 2, 4, 8, 0x10, 0x20
		r, w, x                                     = 4, 2, 1
	)
	got, err := parsePOSIXACL(acl([3]uint32{userObj, 7, 0}, [3]uint32{user, w, 1234}, [3]uint32{groupObj, 5, 0}, [3]uint32{mask, 7, 0}, [3]uint32{other, 5, 0}), 1000)
	require.NoError(t, err)
	assert.Equal(t, []string{"user 1234"}, got)
	got, err = parsePOSIXACL(acl([3]uint32{user, w, 1000}, [3]uint32{user, w, 0}, [3]uint32{user, r | x, 1234}, [3]uint32{mask, 7, 0}), 1000)
	require.NoError(t, err)
	assert.Empty(t, got, "the user, root and an entry with no write")
	got, err = parsePOSIXACL(acl([3]uint32{group, w, 77}, [3]uint32{mask, r | x, 0}), 1000)
	require.NoError(t, err)
	assert.Empty(t, got, "the mask takes the write away")
	got, err = parsePOSIXACL(acl([3]uint32{group, w, 77}), 1000)
	require.NoError(t, err)
	assert.Equal(t, []string{"group 77"}, got)
	_, err = parsePOSIXACL([]byte{1, 2, 3}, 1000)
	assert.Error(t, err)
	_, err = parsePOSIXACL(append(acl(), 9, 9, 9), 1000)
	assert.Error(t, err)
}
