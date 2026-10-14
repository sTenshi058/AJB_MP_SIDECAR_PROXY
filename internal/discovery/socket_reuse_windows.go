//go:build windows

package discovery

import (
	"syscall"
)

func setDiscoverySocketOptions(_, _ string, raw syscall.RawConn) error {
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		if socketErr == nil {
			socketErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
		}
	}); err != nil {
		return err
	}
	return socketErr
}
