//go:build !windows

package secfile

import "os"

// Writers names who besides its owner and root can change what is in the folder at path: its
// group or everyone, when the mode lets them write. A folder this user owns is not reported here:
// outside Windows that is the normal shape of an app bundle or an unpacked tarball.
func Writers(path string) ([]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var who []string
	if fi.Mode().Perm()&0o020 != 0 {
		who = append(who, "its group")
	}
	if fi.Mode().Perm()&0o002 != 0 {
		who = append(who, "everyone")
	}
	return who, nil
}
