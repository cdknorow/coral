//go:build linux

package secfile

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	aclXattr      = "system.posix_acl_access"
	aclTagUser    = 0x02
	aclTagGroup   = 0x08
	aclTagMask    = 0x10
	aclPermWrite  = 0x02
	aclHeaderSize = 4
)

// pathACLWriters reads path's POSIX access list (the system.posix_acl_access attribute): the named users and groups whose entry grants write and that the mask lets keep it.
// None when the file has no list or the filesystem has none; an error when the list can't be read or understood.
func pathACLWriters(path string, uid int, name string) ([]string, error) {
	buf := make([]byte, 1024)
	n, err := unix.Getxattr(path, aclXattr, buf)
	if errors.Is(err, unix.ERANGE) {
		buf = make([]byte, 64*1024)
		n, err = unix.Getxattr(path, aclXattr, buf)
	}
	if err != nil {
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, err
	}
	return parsePOSIXACL(buf[:n], uid)
}

func parsePOSIXACL(b []byte, uid int) ([]string, error) {
	if len(b) < aclHeaderSize || (len(b)-aclHeaderSize)%8 != 0 || binary.LittleEndian.Uint32(b) != 2 {
		return nil, fmt.Errorf("an access list that is not version 2")
	}
	type ent struct {
		tag, perm uint16
		id        uint32
	}
	var ents []ent
	mask := uint16(0x07)
	for o := aclHeaderSize; o < len(b); o += 8 {
		e := ent{binary.LittleEndian.Uint16(b[o:]), binary.LittleEndian.Uint16(b[o+2:]), binary.LittleEndian.Uint32(b[o+4:])}
		ents = append(ents, e)
		if e.tag == aclTagMask {
			mask = e.perm
		}
	}
	var who []string
	for _, e := range ents {
		if e.perm&aclPermWrite == 0 || mask&aclPermWrite == 0 {
			continue
		}
		switch e.tag {
		case aclTagUser:
			if e.id != 0 && int(e.id) != uid {
				who = append(who, fmt.Sprintf("user %d", e.id))
			}
		case aclTagGroup:
			who = append(who, fmt.Sprintf("group %d", e.id))
		}
	}
	return who, nil
}
