// Package pkginfo reads the version, build number and CPU architectures out
// of the desktop app packages uploaded in the admin panel:
//
//   - Windows: the zip from app/scripts/build_windows.ps1 (Huanyou.exe's
//     VERSIONINFO resource and PE machine type).
//   - macOS: the DMG from app/scripts/build_macos.sh (the app's Info.plist and
//     Mach-O header, read straight from the disk image's HFS+ volume).
//
// Only the standard library is used, so it works on the Linux API server.
package pkginfo

import (
	"errors"
	"sort"
	"strings"
)

// Arch names, shared by both platforms.
const (
	ArchARM64 = "arm64"
	ArchX64   = "x86_64"
	ArchX86   = "x86"
)

// Info is what a package declares about itself.
type Info struct {
	Version string   // "1.2.2"
	Build   int64    // 6 (the pubspec number after +)
	Archs   []string // sorted, e.g. ["arm64", "x86_64"]
	MinOS   string   // macOS LSMinimumSystemVersion; "" on Windows
}

// ABI is the release ABI for the package: "" when it runs natively on every
// architecture the platform supports (a universal macOS app), otherwise the
// single architecture.
func (i *Info) ABI(platform string) string {
	if platform == "macos" && has(i.Archs, ArchARM64) && has(i.Archs, ArchX64) {
		return ""
	}
	if len(i.Archs) == 1 {
		return i.Archs[0]
	}
	return strings.Join(i.Archs, "+")
}

// Inspect reads the package at path for platform ("windows" or "macos").
// Other platforms return (nil, nil).
func Inspect(platform, path string) (*Info, error) {
	switch platform {
	case "windows":
		return Windows(path)
	case "macos":
		return MacDMG(path)
	}
	return nil, nil
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedUnique(list []string) []string {
	sort.Strings(list)
	out := list[:0]
	for i, s := range list {
		if i == 0 || s != list[i-1] {
			out = append(out, s)
		}
	}
	return out
}

var errNotFound = errors.New("not found")
