//go:build windows

package stage

import "errors"

func mkfifo(string) error { return errors.New("no FIFOs on Windows") }
