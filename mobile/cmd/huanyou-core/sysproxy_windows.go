//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The LAN proxy setting (Settings → Network → Proxy, also used by Edge,
// Chrome and most apps) is changed through WinINet's per-connection options,
// which also notifies running programs.

var (
	wininet       = windows.NewLazySystemDLL("wininet.dll")
	procSetOption = wininet.NewProc("InternetSetOptionW")
	procQuery     = wininet.NewProc("InternetQueryOptionW")
	procFree      = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

const (
	optRefresh         = 37
	optSettingsChanged = 39
	optPerConnection   = 75

	perConnFlags         = 1
	perConnProxyServer   = 2
	perConnProxyBypass   = 3
	perConnAutoConfigURL = 4
	perConnFlagsUI       = 10 // what the Settings UI shows, incl. "detect automatically"

	proxyTypeDirect = 1
	proxyTypeProxy  = 2
)

// Local and private addresses never go through the proxy.
var bypassList = strings.Join([]string{
	"localhost", "127.*", "10.*", "192.168.*", "169.254.*",
	"172.16.*", "172.17.*", "172.18.*", "172.19.*", "172.20.*", "172.21.*", "172.22.*", "172.23.*",
	"172.24.*", "172.25.*", "172.26.*", "172.27.*", "172.28.*", "172.29.*", "172.30.*", "172.31.*",
	"<local>",
}, ";")

// INTERNET_PER_CONN_OPTIONW: the value is a union of DWORD / LPWSTR / FILETIME (8 bytes).
type perConnOption struct {
	Option uint32
	_      uint32
	Value  uintptr
}

// INTERNET_PER_CONN_OPTION_LISTW.
type perConnList struct {
	Size        uint32
	Connection  *uint16 // nil = LAN
	OptionCount uint32
	OptionError uint32
	Options     *perConnOption
}

type proxySettings struct {
	Flags         uint32 `json:"flags"`
	Server        string `json:"server"`
	Bypass        string `json:"bypass"`
	AutoConfigURL string `json:"autoconfig_url"`
}

func setProxy(port int) error {
	if !hasBackup() {
		cur, err := queryProxy()
		if err != nil {
			return err
		}
		if err := saveBackup(cur); err != nil {
			return err
		}
	}
	return applyProxy(proxySettings{
		Flags:  proxyTypeDirect | proxyTypeProxy,
		Server: fmt.Sprintf("127.0.0.1:%d", port),
		Bypass: bypassList,
	})
}

func restoreProxy() error {
	var s proxySettings
	ok, err := loadBackup(&s)
	if !ok {
		return err
	}
	if err != nil || s.Flags == 0 {
		s = proxySettings{Flags: proxyTypeDirect} // unreadable backup: switch the proxy off
	}
	if err := applyProxy(s); err != nil {
		return err
	}
	return removeBackup()
}

func queryProxy() (proxySettings, error) {
	opts := []perConnOption{{Option: perConnFlagsUI}, {Option: perConnProxyServer}, {Option: perConnProxyBypass}, {Option: perConnAutoConfigURL}}
	list := perConnList{OptionCount: uint32(len(opts)), Options: &opts[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	size := list.Size
	r, _, err := procQuery.Call(0, optPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		opts[0].Option = perConnFlags // FLAGS_UI is missing before Windows 7
		size = list.Size
		if r, _, err = procQuery.Call(0, optPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size))); r == 0 {
			return proxySettings{}, fmt.Errorf("读取系统代理失败：%w", err)
		}
	}
	runtime.KeepAlive(opts)
	return proxySettings{
		Flags:         uint32(opts[0].Value),
		Server:        takeString(opts[1].Value),
		Bypass:        takeString(opts[2].Value),
		AutoConfigURL: takeString(opts[3].Value),
	}, nil
}

// takeString copies a WinINet-allocated string and frees it.
func takeString(p uintptr) string {
	if p == 0 {
		return ""
	}
	s := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p)))
	procFree.Call(p)
	return s
}

func applyProxy(s proxySettings) error {
	str := func(v string) *uint16 {
		if v == "" {
			return nil
		}
		p, _ := windows.UTF16PtrFromString(v)
		return p
	}
	server, bypass, pac := str(s.Server), str(s.Bypass), str(s.AutoConfigURL)
	opts := []perConnOption{
		{Option: perConnFlags, Value: uintptr(s.Flags)},
		{Option: perConnProxyServer, Value: uintptr(unsafe.Pointer(server))},
		{Option: perConnProxyBypass, Value: uintptr(unsafe.Pointer(bypass))},
		{Option: perConnAutoConfigURL, Value: uintptr(unsafe.Pointer(pac))},
	}
	list := perConnList{OptionCount: uint32(len(opts)), Options: &opts[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	r, _, err := procSetOption.Call(0, optPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(list.Size))
	runtime.KeepAlive(opts)
	runtime.KeepAlive(server)
	runtime.KeepAlive(bypass)
	runtime.KeepAlive(pac)
	if r == 0 {
		return err
	}
	procSetOption.Call(0, optSettingsChanged, 0, 0)
	procSetOption.Call(0, optRefresh, 0, 0)
	return nil
}

// onPrimary is only used on macOS (restore reports early there).
var onPrimary func()
