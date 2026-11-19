//go:build linux

package server

import (
	"net"
	"syscall"
	"unsafe"
)

// roomReserve is the socket space kept free for a final notice.
const roomReserve = 16 << 10

// socketRoom returns a function that reports whether n more bytes can be
// written to conn now without blocking, keeping roomReserve free. Bytes the
// peer has not read count against the send buffer, with overhead, so twice n
// is asked for. A frame always fits an empty socket. It returns nil when conn
// cannot be asked.
func socketRoom(conn net.Conn) func(n int) bool {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return nil
	}
	var sndbuf int
	var optErr error
	if err := rc.Control(func(fd uintptr) {
		sndbuf, optErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	}); err != nil || optErr != nil {
		return nil
	}
	return func(n int) bool {
		var queued int32
		var errno syscall.Errno
		if err := rc.Control(func(fd uintptr) {
			// TIOCOUTQ is SIOCOUTQ: the bytes the peer has yet to read.
			_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCOUTQ, uintptr(unsafe.Pointer(&queued)))
		}); err != nil || errno != 0 {
			return true
		}
		return queued == 0 || int(queued)+2*n+roomReserve <= sndbuf
	}
}
