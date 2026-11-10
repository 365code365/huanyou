package pkginfo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// The setup loader, its version resource and the start of the Inno setup
// data all sit in the first few MB; the compressed payload follows.
const installerScan = 32 << 20

// File names like huanyou-windows-1.2.7-12-x64-setup.exe (build_windows.ps1
// and the installer script use this pattern).
var (
	nameVersion = regexp.MustCompile(`(?:^|[^0-9.])(\d{1,4}\.\d{1,4}\.\d{1,4})[-_+](\d{1,6})(?:[^0-9]|$)`)
	nameARM     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(arm64|aarch64)(?:[^a-z0-9]|$)`)
	nameX64     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(x64|x86_64|amd64|win64)(?:[^a-z0-9]|$)`)
	textVersion = regexp.MustCompile(`^(\d{1,4}\.\d{1,4}\.\d{1,4})(?:[.+](\d{1,6}))?$`)
)

// WindowsInstaller reads an Inno Setup installer .exe. The setup stub is a
// 32-bit program whatever it installs, and its payload is compressed, so:
//
//   - version and build: the stub's VS_FIXEDFILEINFO when the script sets
//     VersionInfoVersion=1.2.7.12 (Inno's default is 0.0.0.0), else the file
//     name (…-1.2.7-12-…), cross-checked with the ProductVersion text Inno
//     fills from AppVersion;
//   - CPU: x64 / arm64 in the file name, x86_64 when it has neither.
//
// The VPN core inside can't be checked without decompressing the payload.
func WindowsInstaller(file, name string) (*Info, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, installerScan))
	if err != nil {
		return nil, err
	}
	arch, err := peMachine(b)
	if err != nil {
		return nil, err
	}
	if !isInnoSetup(b) {
		if arch != ArchX86 && bytes.Contains(b, utf16le("Huanyou.exe")) {
			return nil, errors.New("这是 Huanyou.exe 程序本身，不是安装程序。请上传 Inno Setup 打出的 setup.exe，或 build_windows.ps1 打出的 zip")
		}
		return nil, errors.New("只支持 Inno Setup 打包的安装程序（App 内更新会用 /VERYSILENT 静默安装）")
	}

	var ver string
	var build int64
	src := ""
	if ms, ls, ok := fixedFileVersion(b); ok && ls&0xffff != 0 {
		ver, build, src = fmt.Sprintf("%d.%d.%d", ms>>16, ms&0xffff, ls>>16), int64(ls&0xffff), "版本信息"
	}
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if m := nameVersion.FindStringSubmatch(base); m != nil {
		nb, _ := strconv.ParseInt(m[2], 10, 64)
		switch {
		case ver == "":
			ver, build, src = m[1], nb, "文件名"
		case ver != m[1] || build != nb:
			return nil, fmt.Errorf("安装程序的版本信息是 %s（%d），文件名里是 %s（%d），请确认上传的是哪个版本", ver, build, m[1], nb)
		}
	}
	text := versionString(b, "ProductVersion")
	if text == "" {
		text = versionString(b, "FileVersion")
	}
	if m := textVersion.FindStringSubmatch(text); m != nil && text != "0.0.0.0" {
		tb, _ := strconv.ParseInt(m[2], 10, 64)
		switch {
		case ver == "" && tb > 0:
			ver, build, src = m[1], tb, "版本信息"
		case ver != "" && m[1] != ver:
			return nil, fmt.Errorf("安装程序的 AppVersion 是 %s，但%s里是 %s（%d）", text, src, ver, build)
		}
	}
	if ver == "" || build <= 0 {
		return nil, errors.New("识别不到版本号和构建号：请在 .iss 里设置 VersionInfoVersion=1.2.7.12（版本号.构建号），或保持文件名 huanyou-windows-1.2.7-12-x64-setup.exe 的格式")
	}
	cpu := ArchX64
	if nameARM.MatchString(base) {
		cpu = ArchARM64
	} else if !nameX64.MatchString(base) && arch == ArchARM64 {
		cpu = ArchARM64
	}
	return &Info{Version: ver, Build: build, Archs: []string{cpu}, Ext: ".exe", Installer: true}, nil
}

// isInnoSetup: the setup data header ("Inno Setup Setup Data (6.x.x)") or
// the stub's version-info comment ("…built with Inno Setup.").
func isInnoSetup(b []byte) bool {
	return bytes.Contains(b, []byte("Inno Setup Setup Data")) || bytes.Contains(b, utf16le("Inno Setup"))
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(out[2*i:], c)
	}
	return out
}

// versionString reads a StringFileInfo value: a String block is wLength,
// wValueLength, wType, the UTF-16 key with NUL, padding to 32 bits, the
// UTF-16 value.
func versionString(b []byte, key string) string {
	k := append(utf16le(key), 0, 0)
	for i := 0; ; {
		j := bytes.Index(b[i:], k)
		if j < 0 {
			return ""
		}
		p := i + j
		i = p + 1
		start := p - 6 // the block header precedes the key
		if start < 0 || start%4 != 0 {
			continue
		}
		v := p + len(k)
		v += (4 - (v-start)%4) % 4
		var u []uint16
		for ; v+1 < len(b) && len(u) < 64; v += 2 {
			c := binary.LittleEndian.Uint16(b[v:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		return strings.TrimSpace(string(utf16.Decode(u)))
	}
}
