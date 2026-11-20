//go:build !linux

package server

import "net"

// socketRoom cannot ask the socket how much its peer has yet to read on this
// platform, so events are written as they come.
func socketRoom(net.Conn) func(n int, strict bool) bool { return nil }
