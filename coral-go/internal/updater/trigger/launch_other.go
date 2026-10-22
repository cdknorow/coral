//go:build !windows

package trigger

import (
	"context"
	"errors"
)

// ErrUnsupported is Launch's answer off Windows: only Windows installs an update from the dashboard; elsewhere the apply needs no one to approve it.
var ErrUnsupported = errors.New("this computer does not install updates from the dashboard")

// Launch starts nothing off Windows.
func Launch(context.Context, string, string) error { return ErrUnsupported }
