//go:build !darwin

package vpncore

// FindTunFd is only needed on iOS; Android gets the fd from VpnService.
func FindTunFd() int32 { return -1 }
