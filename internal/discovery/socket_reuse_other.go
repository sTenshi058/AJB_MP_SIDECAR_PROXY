//go:build !linux && !darwin && !windows

package discovery

import (
	"errors"
	"syscall"
)

func setReuseAddress(string, string, syscall.RawConn) error {
	return errors.New("UDP discovery monitoring is unsupported on this operating system")
}
