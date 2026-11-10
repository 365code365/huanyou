package pkginfo

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const (
	winAppExe  = "huanyou.exe"
	winCoreExe = "huanyou-core.exe"
	maxExe     = 64 << 20
)

// Windows reads a Windows package, told apart by its first bytes: the zipped
// release folder (PK) or an Inno Setup installer .exe (MZ). name is the
// uploaded file name, used by installers whose version resource lacks the
// build number.
func Windows(file, name string) (*Info, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 2)
	_, err = io.ReadFull(f, head)
	f.Close()
	if err != nil {
		return nil, errors.New("文件太小，不是安装包")
	}
	switch string(head) {
	case "PK":
		return windowsZip(file)
	case "MZ":
		return WindowsInstaller(file, name)
	}
	return nil, errors.New("不是 zip 也不是 exe 安装程序")
}

// windowsZip reads a zipped Flutter release folder: Huanyou.exe (version from
// its VS_FIXEDFILEINFO, which Flutter fills from pubspec "1.2.2+6" as
// 1.2.2.6) and the VPN core next to it.
func windowsZip(file string) (*Info, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return nil, errors.New("不是有效的 zip 文件")
	}
	defer zr.Close()

	// Windows PowerShell 5's Compress-Archive writes "\" separators.
	name := func(f *zip.File) string { return strings.ReplaceAll(f.Name, "\\", "/") }
	var app *zip.File
	for _, f := range zr.File {
		if strings.EqualFold(path.Base(name(f)), winAppExe) && (app == nil || len(f.Name) < len(app.Name)) {
			app = f
		}
	}
	if app == nil {
		return nil, errors.New("zip 里没有 Huanyou.exe，请上传 build_windows.ps1 打出的包")
	}
	dir := path.Dir(name(app))
	var core *zip.File
	for _, f := range zr.File {
		if path.Dir(name(f)) == dir && strings.EqualFold(path.Base(name(f)), winCoreExe) {
			core = f
		}
	}
	if core == nil {
		return nil, errors.New("zip 里 Huanyou.exe 旁边缺少 VPN 核心 huanyou-core.exe")
	}

	exe, err := readZipFile(app, maxExe)
	if err != nil {
		return nil, err
	}
	arch, err := peMachine(exe)
	if err != nil {
		return nil, fmt.Errorf("Huanyou.exe：%w", err)
	}
	ms, ls, ok := fixedFileVersion(exe)
	if !ok {
		return nil, errors.New("Huanyou.exe 里没有版本信息")
	}
	info := &Info{
		Version: fmt.Sprintf("%d.%d.%d", ms>>16, ms&0xffff, ls>>16),
		Build:   int64(ls & 0xffff),
		Archs:   []string{arch},
		Ext:     ".zip",
	}
	if info.Build == 0 {
		return nil, errors.New("Huanyou.exe 没有构建号：pubspec.yaml 的 version 要写成 1.2.0+5 的形式")
	}
	return info, nil
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s 太大", path.Base(f.Name))
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

// peMachine returns the CPU architecture in the PE header.
func peMachine(b []byte) (string, error) {
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return "", errors.New("不是 Windows 程序")
	}
	off := int(binary.LittleEndian.Uint32(b[0x3c:]))
	if off < 0 || off+6 > len(b) || string(b[off:off+4]) != "PE\x00\x00" {
		return "", errors.New("PE 头损坏")
	}
	switch m := binary.LittleEndian.Uint16(b[off+4:]); m {
	case 0x8664:
		return ArchX64, nil
	case 0xaa64:
		return ArchARM64, nil
	case 0x014c:
		return ArchX86, nil
	default:
		return "", fmt.Errorf("未知的 CPU 类型 0x%04x", m)
	}
}

// fixedFileVersion finds VS_FIXEDFILEINFO (signature 0xFEEF04BD, struct
// version 1.0) and returns dwFileVersionMS / LS.
func fixedFileVersion(b []byte) (ms, ls uint32, ok bool) {
	sig := []byte{0xbd, 0x04, 0xef, 0xfe, 0x00, 0x00, 0x01, 0x00}
	for i := 0; ; {
		j := bytes.Index(b[i:], sig)
		if j < 0 {
			return 0, 0, false
		}
		p := i + j
		if p+16 <= len(b) && p%4 == 0 { // resource data is DWORD-aligned
			return binary.LittleEndian.Uint32(b[p+8:]), binary.LittleEndian.Uint32(b[p+12:]), true
		}
		i = p + 1
	}
}
