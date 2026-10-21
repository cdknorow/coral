//go:build !unix

package main

import (
	"errors"
	"os"
)

// The helper runs on the operator's Mac or a Linux host: the owner and mode checks need Unix.
func checkOwner(string, os.FileInfo, int) error {
	return errors.New("this helper needs a Unix file system to check the key's owner and mode")
}

func openNoFollow(string) (*os.File, error) {
	return nil, errors.New("this helper needs a Unix file system")
}
