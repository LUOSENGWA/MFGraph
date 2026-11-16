//go:build !unix

package main

import "net"

func listenUnixPrivate(path string) (net.Listener, error) { return net.Listen("unix", path) }
