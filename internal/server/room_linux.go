//go:build linux

package server

import (
	"net"
	"syscall"
	"unsafe"

	"github.com/boldfield/syndic/internal/wire"
)

// roomReserve is the socket space kept free for a final notice.
const roomReserve = 16 << 10

// socketRoom returns a function that reports whether n more bytes can be
// written to conn now without blocking, keeping roomReserve free. Bytes the
// peer has not read count against the send buffer, with overhead, so twice n
// is asked for. The send buffer is first raised, as far as the kernel allows,
// to hold a frame of the largest size and the reserve. A frame that still
// does not fit is let through once the peer has read everything, unless
// strict; it may then block partway if the peer stops reading. It returns nil
// when conn cannot be asked.
func socketRoom(conn net.Conn) func(n int, strict bool) bool {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return nil
	}
	sndbuf, err := sendBuffer(rc)
	if err != nil {
		return nil
	}
	if want := 2 * (wire.MaxFrameBytes + roomReserve); sndbuf < want {
		// The kernel doubles the value asked for, for its overhead, and caps
		// it at net.core.wmem_max; whatever it settles on is read back.
		_ = uc.SetWriteBuffer(want / 2)
		if sndbuf, err = sendBuffer(rc); err != nil {
			return nil
		}
	}
	return func(n int, strict bool) bool {
		var queued int32
		var errno syscall.Errno
		if err := rc.Control(func(fd uintptr) {
			// TIOCOUTQ is SIOCOUTQ: the bytes the peer has yet to read.
			_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCOUTQ, uintptr(unsafe.Pointer(&queued)))
		}); err != nil || errno != 0 {
			return !strict
		}
		return (queued == 0 && !strict) || int(queued)+2*n+roomReserve <= sndbuf
	}
}

// sendBuffer reads the socket's SO_SNDBUF.
func sendBuffer(rc syscall.RawConn) (int, error) {
	var sndbuf int
	var optErr error
	if err := rc.Control(func(fd uintptr) {
		sndbuf, optErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	}); err != nil {
		return 0, err
	}
	return sndbuf, optErr
}
