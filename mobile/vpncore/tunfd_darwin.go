//go:build darwin

package vpncore

import "golang.org/x/sys/unix"

// FindTunFd returns the utun file descriptor that NEPacketTunnelProvider
// created for this extension, or -1. Apple has no public API for it, so we
// scan open fds for the utun kernel-control socket (same approach as
// WireGuard and sing-box).
func FindTunFd() int32 {
	info := &unix.CtlInfo{}
	const name = "com.apple.net.utun_control"
	copy(info.Name[:], name)
	for fd := 0; fd < 1024; fd++ {
		sa, err := unix.Getpeername(fd)
		if err != nil {
			continue
		}
		ctl, ok := sa.(*unix.SockaddrCtl)
		if !ok {
			continue
		}
		if info.Id == 0 {
			if err := unix.IoctlCtlInfo(fd, info); err != nil {
				continue
			}
		}
		if ctl.ID == info.Id {
			return int32(fd)
		}
	}
	return -1
}
