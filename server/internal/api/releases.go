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
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"vpnplatform/server/internal/store"
)

// App releases: the admin uploads an APK / IPA (or links an external URL),
// the app checks GET /api/v1/app/update, and the public download site lists
// the latest build per platform.

const maxPackageBytes = 600 << 20 // 600 MB

var versionPattern = regexp.MustCompile(`^\d{1,4}(\.\d{1,4}){0,3}$`)

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
}

func (s *Server) releaseView(r *store.Release) releaseView { return releaseView{r, s.releaseURL(r)} }

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
	var tmp string
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
			path, size, sum, err := s.saveUpload(part)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "bad_package", err.Error())
				return
			}
			tmp, rel.Size, rel.SHA256 = path, size, sum
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
		}
	}

	switch {
	case rel.Platform != "android" && rel.Platform != "ios":
		writeErr(w, http.StatusBadRequest, "bad_platform", "平台只能是 android 或 ios")
		return
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
		ext := map[string]string{"android": ".apk", "ios": ".ipa"}[rel.Platform]
		rel.File = fmt.Sprintf("huanyou-%s-%s-%d%s", rel.Platform, rel.Version, rel.Build, ext)
		rel.URL = ""
	}
	if latest, err := s.store.LatestRelease(r.Context(), rel.Platform); err == nil && rel.Build <= latest.Build {
		writeErr(w, http.StatusConflict, "build_not_newer",
			fmt.Sprintf("构建号必须大于当前最新的 %d（%s）", latest.Build, latest.Version))
		return
	}
	created, err := s.store.CreateRelease(r.Context(), rel)
	if errors.Is(err, store.ErrBuildExists) {
		writeErr(w, http.StatusConflict, "build_exists", "该平台已存在这个构建号")
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

// saveUpload streams a package to a temp file, checking it is a ZIP (APK and
// IPA are both ZIP archives) and hashing it.
func (s *Server) saveUpload(part *multipart.Part) (path string, size int64, sum string, err error) {
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
	if n < 4 || string(head[:2]) != "PK" {
		f.Close()
		os.Remove(path)
		return "", 0, "", errors.New("文件不是有效的 APK / IPA（不是 ZIP 格式）")
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
	if err := s.store.UpdateRelease(r.Context(), id, cur.Notes, cur.Force, cur.Published); err != nil {
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

// handleAppUpdate: GET /api/v1/app/update?platform=android&build=3
func (s *Server) handleAppUpdate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	platform := q.Get("platform")
	if platform != "android" && platform != "ios" {
		writeErr(w, http.StatusBadRequest, "bad_platform", "platform 只能是 android 或 ios")
		return
	}
	build, _ := strconv.ParseInt(q.Get("build"), 10, 64)
	latest, err := s.store.LatestRelease(r.Context(), platform)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"has_update": false})
		return
	} else if err != nil {
		s.internal(w, err)
		return
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
			"url": s.releaseURL(latest), "size": latest.Size, "sha256": latest.SHA256,
			"published_at": latest.CreatedAt,
		},
	})
}

// ---- files ----

var releaseFile = regexp.MustCompile(`^huanyou-(android|ios)-[0-9.]+-\d+\.(apk|ipa)$`)

// handleReleaseFile: GET /dl/releases/{name}
func (s *Server) handleReleaseFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
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
