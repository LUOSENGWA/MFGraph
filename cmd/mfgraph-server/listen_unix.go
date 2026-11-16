//go:build unix

package main

import (
	"net"
	"syscall"
)

// listenUnixPrivate creates the socket with the umask 0177, so that it is
// never accessible to others, not even before the chmod to 0600.
func listenUnixPrivate(path string) (net.Listener, error) {
	old := syscall.Umask(0o177)
	defer syscall.Umask(old)
	return net.Listen("unix", path)
}
