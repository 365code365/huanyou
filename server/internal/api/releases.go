package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"vpnplatform/server/internal/pkginfo"
	"vpnplatform/server/internal/store"
)

// App releases: the admin uploads a package (or links an external URL), the
// app checks GET /api/v1/app/update, and the public download site lists the
// latest build per platform.

const maxPackageBytes = 600 << 20 // 600 MB

var versionPattern = regexp.MustCompile(`^\d{1,4}(\.\d{1,4}){0,3}$`)

// abiSpec is one CPU build a platform can publish.
type abiSpec struct {
	ABI   string // "" = universal / runs everywhere
	Label string // download site, e.g. "64 位版"
}

// platformSpec describes how a platform's packages are published.
type platformSpec struct {
	Ext string
	// Magic: accepted first bytes of an upload ("PK" ZIP, "MZ" Windows exe);
	// none = checked by pkginfo once saved (DMG).
	Magic []string
	// Inspect: version, build and ABI are read from the package itself
	// (internal/pkginfo) instead of being typed in.
	Inspect bool
	// ABIs allowed, in download-site order (the first published one is the
	// main download button).
	ABIs []abiSpec
}

var platforms = map[string]platformSpec{
	// Flutter's --split-per-abi APKs, or one universal APK.
	"android": {Ext: ".apk", Magic: []string{"PK"}, ABIs: []abiSpec{
		{"", "通用版"}, {"arm64-v8a", "64 位版"}, {"armeabi-v7a", "32 位版（老款手机）"}, {"x86_64", "x86 版（模拟器 / 平板）"},
	}},
	"ios": {Ext: ".ipa", Magic: []string{"PK"}, ABIs: []abiSpec{{"", ""}}},
	// The zipped release folder from app/scripts/build_windows.ps1, or an
	// Inno Setup installer .exe (pkginfo tells them apart).
	"windows": {Ext: ".zip", Magic: []string{"PK", "MZ"}, Inspect: true, ABIs: []abiSpec{
		{pkginfo.ArchX64, "x64 版（Intel / AMD）"}, {pkginfo.ArchARM64, "ARM 版（骁龙等）"},
	}},
	// The drag-to-Applications disk image from app/scripts/build_macos.sh.
	"macos": {Ext: ".dmg", Inspect: true, ABIs: []abiSpec{
		{"", "通用版（Apple 芯片 + Intel）"}, {pkginfo.ArchARM64, "Apple 芯片版"}, {pkginfo.ArchX64, "Intel 版"},
	}},
}

// knownPlatform reports whether p is an app platform (android, ios, windows, macos).
func knownPlatform(p string) bool { _, ok := platforms[p]; return ok }

// abiIndex is the ABI's position in p.ABIs (download-site order), -1 if not allowed.
func (p platformSpec) abiIndex(abi string) int {
	for i, a := range p.ABIs {
		if a.ABI == abi {
			return i
		}
	}
	return -1
}

func (p platformSpec) allows(abi string) bool { return p.abiIndex(abi) >= 0 }

func (p platformSpec) abiError() string {
	var names []string
	for _, a := range p.ABIs {
		if a.ABI == "" {
			names = append(names, "留空（通用）")
		} else {
			names = append(names, a.ABI)
		}
	}
	return "CPU 架构只能是 " + strings.Join(names, "、")
}

func (s *Server) releaseDir() string { return filepath.Join(s.cfg.DownloadDir, "releases") }

// releaseURL is where a client downloads / installs the release.
func (s *Server) releaseURL(r *store.Release) string {
	switch {
	case r.Platform == "ios" && r.File != "":
		// Ad-hoc / enterprise IPA: Safari installs via the generated manifest.
		return "itms-services://?action=download-manifest&url=" + s.cfg.PublicURL + "/dl/releases/" + strconv.FormatInt(r.ID, 10) + "/manifest.plist"
	case r.File != "":
		return s.cfg.PublicURL + "/dl/releases/" + r.File
	default:
		return r.URL
	}
}

type releaseView struct {
	*store.Release
	DownloadURL string `json:"download_url"`
	Kind        string `json:"kind"` // apk | ipa | zip | dmg | exe | ""
}

func (s *Server) releaseView(r *store.Release) releaseView {
	return releaseView{r, s.releaseURL(r), releaseKind(r)}
}

// ---- admin ----

func (s *Server) adminListReleases(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Releases(r.Context(), r.URL.Query().Get("platform"))
	if err != nil {
		s.internal(w, err)
		return
	}
	items := make([]releaseView, 0, len(list))
	for _, x := range list {
		items = append(items, s.releaseView(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// adminCreateRelease accepts multipart/form-data: platform, version, build,
// notes, force, bundle_id, url, and optionally the package as "file". Fields
// must come before the file part (the browser sends them in FormData order);
// the file is streamed to disk, hashed on the way.
func (s *Server) adminCreateRelease(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(31 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, maxPackageBytes+1<<20)

	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请使用 multipart/form-data 上传")
		return
	}
	rel := &store.Release{Published: true}
	var tmp, upName string
	defer func() {
		if tmp != "" {
			os.Remove(tmp)
		}
	}()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "上传中断："+err.Error())
			return
		}
		if part.FormName() == "file" {
			if rel.Platform == "" {
				writeErr(w, http.StatusBadRequest, "bad_request", "请先提交 platform 等字段，再提交文件")
				return
			}
			spec, ok := platforms[rel.Platform]
			if !ok {
				writeErr(w, http.StatusBadRequest, "bad_platform", "平台只能是 android、ios、windows 或 macos")
				return
			}
			path, size, sum, err := s.saveUpload(part, spec)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "bad_package", err.Error())
				return
			}
			tmp, rel.Size, rel.SHA256, upName = path, size, sum, part.FileName()
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(part, 8<<10))
		v := strings.TrimSpace(string(b))
		switch part.FormName() {
		case "platform":
			rel.Platform = v
		case "version":
			rel.Version = v
		case "build":
			rel.Build, _ = strconv.ParseInt(v, 10, 64)
		case "notes":
			rel.Notes = v
		case "force":
			rel.Force = v == "true" || v == "1"
		case "published":
			rel.Published = v != "false" && v != "0"
		case "bundle_id":
			rel.BundleID = v
		case "url":
			rel.URL = v
		case "abi":
			rel.ABI = v
		}
	}
	spec, ok := platforms[rel.Platform]
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_platform", "平台只能是 android、ios、windows 或 macos")
		return
	}
	ext := spec.Ext
	if tmp != "" && spec.Inspect {
		info, err := pkginfo.Inspect(rel.Platform, tmp, upName)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_package", "无法识别安装包："+err.Error())
			return
		}
		// Typed-in values are optional; if given they must match the package.
		if rel.Version != "" && rel.Version != info.Version {
			writeErr(w, http.StatusBadRequest, "version_mismatch", fmt.Sprintf("填写的版本号 %s 与安装包里的 %s 不一致", rel.Version, info.Version))
			return
		}
		if rel.Build != 0 && rel.Build != info.Build {
			writeErr(w, http.StatusBadRequest, "build_mismatch", fmt.Sprintf("填写的构建号 %d 与安装包里的 %d 不一致", rel.Build, info.Build))
			return
		}
		rel.Version, rel.Build, rel.ABI = info.Version, info.Build, info.ABI(rel.Platform)
		if info.Installer {
			ext = installerSuffix
		} else if info.Ext != "" {
			ext = info.Ext
		}
	}
	if !spec.allows(rel.ABI) {
		writeErr(w, http.StatusBadRequest, "bad_abi", spec.abiError())
		return
	}
	switch {
	case !versionPattern.MatchString(rel.Version):
		writeErr(w, http.StatusBadRequest, "bad_version", "版本号格式应为 1.2.3")
		return
	case rel.Build <= 0:
		writeErr(w, http.StatusBadRequest, "bad_build", "构建号必须是正整数，且每次发版递增")
		return
	case tmp == "" && rel.URL == "":
		writeErr(w, http.StatusBadRequest, "no_package", "请上传安装包或填写下载链接")
		return
	case rel.URL != "" && !strings.HasPrefix(rel.URL, "https://") && !strings.HasPrefix(rel.URL, "itms-services://"):
		writeErr(w, http.StatusBadRequest, "bad_url", "下载链接必须是 https 地址")
		return
	case tmp != "" && rel.Platform == "ios" && rel.BundleID == "":
		writeErr(w, http.StatusBadRequest, "bad_bundle", "上传 IPA 需要填写 Bundle ID（用于生成安装清单）")
		return
	}
	if tmp != "" {
		rel.File = fmt.Sprintf("huanyou-%s-%s-%d%s", rel.Platform, rel.Version, rel.Build, ext)
		if rel.ABI != "" {
			rel.File = fmt.Sprintf("huanyou-%s-%s-%d-%s%s", rel.Platform, rel.Version, rel.Build, rel.ABI, ext)
		}
		rel.URL = ""
	}
	// The same build may be uploaded once per ABI; otherwise builds must grow.
	if latest, err := s.store.LatestRelease(r.Context(), rel.Platform, ""); err == nil && rel.Build < latest.Build {
		writeErr(w, http.StatusConflict, "build_not_newer",
			fmt.Sprintf("构建号不能小于当前最新的 %d（%s）", latest.Build, latest.Version))
		return
	}
	created, err := s.store.CreateRelease(r.Context(), rel)
	if errors.Is(err, store.ErrBuildExists) {
		writeErr(w, http.StatusConflict, "build_exists", "这个构建号（同一 CPU 架构）已经发布过")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	if tmp != "" {
		if err := os.Rename(tmp, filepath.Join(s.releaseDir(), rel.File)); err != nil {
			_ = s.store.DeleteRelease(r.Context(), created.ID)
			s.internal(w, err)
			return
		}
		tmp = ""
	}
	slog.Info("release created", "platform", created.Platform, "version", created.Version, "build", created.Build, "size", created.Size)
	writeJSON(w, http.StatusOK, s.releaseView(created))
}

// saveUpload streams a package to a temp file and hashes it. ZIP-based
// packages are checked by their first bytes; the others (DMG) are checked by
// pkginfo once saved.
func (s *Server) saveUpload(part *multipart.Part, spec platformSpec) (path string, size int64, sum string, err error) {
	if err := os.MkdirAll(s.releaseDir(), 0o755); err != nil {
		return "", 0, "", err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	path = filepath.Join(s.releaseDir(), ".upload-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return "", 0, "", err
	}
	h := sha256.New()
	head := make([]byte, 4)
	n, _ := io.ReadFull(part, head)
	if len(spec.Magic) > 0 && (n < 4 || !slices.Contains(spec.Magic, string(head[:2]))) {
		f.Close()
		os.Remove(path)
		if slices.Contains(spec.Magic, "MZ") {
			return "", 0, "", errors.New("文件不是 zip 压缩包，也不是 exe 安装程序")
		}
		return "", 0, "", fmt.Errorf("文件不是有效的 %s 安装包（不是 ZIP 格式）", strings.ToUpper(strings.TrimPrefix(spec.Ext, ".")))
	}
	w := io.MultiWriter(f, h)
	w.Write(head[:n])
	copied, err := io.Copy(w, io.LimitReader(part, maxPackageBytes))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", 0, "", fmt.Errorf("保存失败：%w", err)
	}
	size = copied + int64(n)
	if size > maxPackageBytes {
		os.Remove(path)
		return "", 0, "", errors.New("安装包超过 600 MB")
	}
	return path, size, hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Server) adminPatchRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cur, err := s.store.ReleaseByID(r.Context(), id)
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	var body struct {
		Notes     *string `json:"notes"`
		Force     *bool   `json:"force"`
		Published *bool   `json:"published"`
		ABI       *string `json:"abi"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Notes != nil {
		cur.Notes = strings.TrimSpace(*body.Notes)
	}
	if body.Force != nil {
		cur.Force = *body.Force
	}
	if body.Published != nil {
		cur.Published = *body.Published
	}
	if body.ABI != nil {
		if spec := platforms[cur.Platform]; !spec.allows(*body.ABI) {
			writeErr(w, http.StatusBadRequest, "bad_abi", spec.abiError())
			return
		}
		cur.ABI = *body.ABI
	}
	if err := s.store.UpdateRelease(r.Context(), id, cur.Notes, cur.Force, cur.Published, cur.ABI); err != nil {
		s.adminStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.releaseView(cur))
}

func (s *Server) adminDeleteRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cur, err := s.store.ReleaseByID(r.Context(), id)
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	if err := s.store.DeleteRelease(r.Context(), id); err != nil {
		s.adminStoreErr(w, err)
		return
	}
	if cur.File != "" {
		_ = os.Remove(filepath.Join(s.releaseDir(), cur.File))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- app ----

// handleAppUpdate: GET /api/v1/app/update?platform=android&build=3&abi=arm64-v8a
// abi is the device CPU (desktop: arm64 / x86_64).
func (s *Server) handleAppUpdate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	platform := q.Get("platform")
	spec, ok := platforms[platform]
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_platform", "platform 只能是 android、ios、windows 或 macos")
		return
	}
	build, _ := strconv.ParseInt(q.Get("build"), 10, 64)
	abi := q.Get("abi")
	if !spec.allows(abi) {
		abi = ""
	}
	latest, err := s.store.LatestRelease(r.Context(), platform, abi)
	if errors.Is(err, store.ErrNotFound) && spec.Inspect && abi == pkginfo.ArchARM64 {
		// ARM desktops run x64 builds too (Windows on ARM emulation, Rosetta).
		latest, err = s.store.LatestRelease(r.Context(), platform, pkginfo.ArchX64)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"has_update": false})
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	kind := releaseKind(latest)
	sum := latest.SHA256
	// Apps from before installer support would try to unzip a setup.exe:
	// without a checksum they open the browser download instead.
	if kind == "exe" && q.Get("installer") != "1" {
		sum = ""
	}
	has := latest.Build > build
	force := false
	if has {
		if force, err = s.store.ForcedSince(r.Context(), platform, build); err != nil {
			s.internal(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"has_update": has,
		"force":      force,
		"latest": map[string]any{
			"version": latest.Version, "build": latest.Build, "notes": latest.Notes,
			"url": s.releaseURL(latest), "size": latest.Size, "sha256": sum,
			"published_at": latest.CreatedAt, "kind": kind,
		},
	})
}

// ---- files ----

var releaseFile = regexp.MustCompile(`^huanyou-(android|ios|windows|macos)-[0-9.]+-\d+(-(arm64-v8a|armeabi-v7a|x86_64|arm64))?(\.(apk|ipa|zip|dmg)|-setup\.exe)$`)

// installerSuffix ends the stored name of a Windows installer .exe.
const installerSuffix = "-setup.exe"

// releaseKind is the package type by file name: apk, ipa, zip, dmg, exe, or
// "" for an external link without a recognizable extension.
func releaseKind(r *store.Release) string {
	name := r.File
	if name == "" {
		if u, err := url.Parse(r.URL); err == nil {
			name = u.Path
		}
	}
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".apk", ".ipa", ".zip", ".dmg", ".exe":
		return ext[1:]
	}
	return ""
}

// handleReleaseFile: GET /dl/releases/{name}
func (s *Server) handleReleaseFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	allowSlowDownload(w)
	if !releaseFile.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.releaseDir(), name)
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".apk") {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	} else if strings.HasSuffix(name, ".zip") {
		w.Header().Set("Content-Type", "application/zip")
	} else if strings.HasSuffix(name, ".dmg") {
		w.Header().Set("Content-Type", "application/x-apple-diskimage")
	} else if strings.HasSuffix(name, ".exe") {
		w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // names include the build
	http.ServeFile(w, r, p)
}

// handleIOSManifest: GET /dl/releases/{id}/manifest.plist — the OTA install manifest.
func (s *Server) handleIOSManifest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rel, err := s.store.ReleaseByID(r.Context(), id)
	if err != nil || rel.Platform != "ios" || rel.File == "" || !rel.Published {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = manifestTmpl.Execute(w, map[string]string{
		"URL": s.cfg.PublicURL + "/dl/releases/" + rel.File, "BundleID": rel.BundleID,
		"Version": rel.Version, "Title": AppName,
	})
}

var manifestTmpl = template.Must(template.New("m").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>items</key><array><dict>
<key>assets</key><array><dict><key>kind</key><string>software-package</string><key>url</key><string>{{.URL}}</string></dict></array>
<key>metadata</key><dict><key>bundle-identifier</key><string>{{.BundleID}}</string>
<key>bundle-version</key><string>{{.Version}}</string><key>kind</key><string>software</string>
<key>title</key><string>{{.Title}}</string></dict>
</dict></array></dict></plist>`))

// allowSlowDownload lifts the server's 30 s write timeout for a package
// download: a 36 MB DMG takes minutes on a slow connection, and the timeout
// cut such downloads off mid-file.
func allowSlowDownload(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Hour))
}
