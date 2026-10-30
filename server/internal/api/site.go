package api

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The public download site: GET / , /download and /i/{code} (invite).
// It lists the latest uploaded build per platform and is clearly labelled as
// an internal test app, not open to the public.

type siteBuild struct {
	Version string
	Build   int64
	Date    string
	Size    string
	Notes   []string
	URL     string
	Label   string        // CPU build of URL when there are several, e.g. "x64 版（Intel / AMD）"
	Others  []siteVariant // other CPU builds of the same version
}

type siteVariant struct {
	Label, Size, URL string
}

// latestBuild resolves what the site offers for a platform, in priority:
// the latest published release, the share-settings link, the legacy APK.
// A build published for several CPUs shows the first ABI of the platform
// spec as the main button and the others as extra links.
func (s *Server) latestBuild(ctx context.Context, platform string) *siteBuild {
	spec := platforms[platform]
	if all, err := s.store.LatestBuilds(ctx, platform); err == nil && len(all) > 0 {
		sort.SliceStable(all, func(i, j int) bool { return spec.abiIndex(all[i].ABI) < spec.abiIndex(all[j].ABI) })
		r := all[0]
		b := &siteBuild{Version: r.Version, Build: r.Build, Date: r.CreatedAt.Format("2006-01-02"), URL: s.releaseURL(r), Size: sizeLabel(r.Size)}
		if len(all) > 1 { // name the main download too when there is a choice
			b.Label = spec.ABIs[max(0, spec.abiIndex(r.ABI))].Label
		}
		for _, x := range all[1:] {
			label := x.ABI
			if i := spec.abiIndex(x.ABI); i >= 0 {
				label = spec.ABIs[i].Label
			}
			b.Others = append(b.Others, siteVariant{Label: label, URL: s.releaseURL(x), Size: sizeLabel(x.Size)})
		}
		for _, l := range strings.Split(r.Notes, "\n") {
			if l = strings.TrimSpace(strings.TrimLeft(l, "-•*· ")); l != "" {
				b.Notes = append(b.Notes, l)
			}
		}
		return b
	}
	share := s.cat().Share
	if platform == "android" {
		if share.AndroidURL != "" {
			return &siteBuild{URL: share.AndroidURL}
		}
		if st, err := os.Stat(filepath.Join(s.cfg.DownloadDir, "app.apk")); err == nil && !st.IsDir() {
			return &siteBuild{URL: "/dl/app.apk", Size: humanBytes(st.Size()), Date: st.ModTime().Format("2006-01-02")}
		}
	}
	if platform == "ios" && share.IOSURL != "" {
		return &siteBuild{URL: share.IOSURL}
	}
	return nil
}

// androidURL is the link the invite share text and older code paths use.
func (s *Server) androidURL() string {
	if b := s.latestBuild(context.Background(), "android"); b != nil {
		return b.URL
	}
	return ""
}

func sizeLabel(n int64) string {
	if n <= 0 {
		return ""
	}
	return humanBytes(n)
}

// humanBytes formats a size like "116.9 MB".
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KB", n>>10)
	}
}

var codePattern = regexp.MustCompile(`^[A-Z2-9]{7}$`)

func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(r.PathValue("code"))
	if !codePattern.MatchString(code) {
		code = ""
	}
	ref := s.cat().Referral
	slogan := s.cat().Share.Slogan
	if slogan == "" {
		slogan = "一键连接，畅游全球"
	}
	data := map[string]any{
		"App": AppName, "Slogan": slogan, "Code": code,
		"Android": s.latestBuild(r.Context(), "android"), "IOS": s.latestBuild(r.Context(), "ios"),
		"Windows":     s.latestBuild(r.Context(), "windows"),
		"Mac":         s.latestBuild(r.Context(), "macos"),
		"InviteeDays": 0, "Year": time.Now().Year(),
	}
	if ref.Enabled && code != "" {
		data["InviteeDays"] = ref.InviteeDays
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow") // internal test app: keep it out of search engines
	if err := landingTmpl.Execute(w, data); err != nil {
		slog.Warn("landing render", "err", err)
	}
}

// handleRobots keeps the internal test site out of search engines.
func (s *Server) handleRobots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

var landingTmpl = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="zh-CN"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="robots" content="noindex,nofollow">
<title>{{.App}} · 内部测试</title>
<style>
*{box-sizing:border-box;margin:0}
body{min-height:100vh;font-family:-apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif;
 background:radial-gradient(900px 520px at 85% -10%,#1f4d3a 0%,transparent 60%),radial-gradient(700px 500px at -10% 110%,#1d2a44 0%,transparent 60%),#0d0f13;
 color:#f2f1ed;display:flex;flex-direction:column;align-items:center;padding:0 20px}
.notice{width:100%;max-width:440px;margin-top:20px;padding:12px 14px;border-radius:14px;
 background:rgba(217,130,43,.12);border:1px solid rgba(217,130,43,.45);color:#f3c27a;font-size:13px;line-height:1.6;display:flex;gap:10px}
.notice b{color:#ffd59a}
.card{width:100%;max-width:440px;text-align:center;padding:30px 0 10px}
.mark{width:76px;height:76px;margin:0 auto 18px;filter:drop-shadow(0 18px 40px rgba(18,161,80,.25))}
h1{font-size:32px;font-weight:800;letter-spacing:4px}
.tag{display:inline-block;margin-left:6px;padding:2px 8px;border-radius:7px;font-size:12px;letter-spacing:0;vertical-align:middle;
 background:#d9822b;color:#0d0f13;font-weight:800}
.slogan{margin-top:8px;color:#a9a8a3;font-size:14px}
.gift{margin:22px 0 4px;padding:16px;border-radius:16px;background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.09)}
.gift small{color:#a9a8a3;font-size:12.5px}.code{margin-top:4px;font:800 28px ui-monospace,Menlo,monospace;letter-spacing:6px}
.gift p{margin-top:6px;color:#7fd8a4;font-size:13px}
.pkg{margin-top:14px;padding:16px;border-radius:18px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.09);text-align:left}
.pkg h3{font-size:16px;display:flex;justify-content:space-between;align-items:baseline}
.pkg h3 span{font-size:12px;color:#8f8e8a;font-weight:500}
.meta{margin-top:4px;color:#8f8e8a;font-size:12px}
.alt{display:block;margin-top:10px;text-align:center;color:#6e6f73;font-size:13px}
.pkg ul{margin:10px 0 0 18px;color:#c9c8c2;font-size:13px;line-height:1.7}
.btn{display:flex;align-items:center;justify-content:center;height:50px;margin-top:12px;border-radius:14px;
 font-size:15.5px;font-weight:700;text-decoration:none;border:0;width:100%}
.primary{background:#f2f1ed;color:#0d0f13}.ghost{background:rgba(255,255,255,.08);color:#f2f1ed;border:1px solid rgba(255,255,255,.12)}
.disabled{opacity:.45;pointer-events:none}
.tip{margin-top:12px;color:#8f8e8a;font-size:12px;line-height:1.7;text-align:left}
footer{width:100%;max-width:440px;margin:26px 0 30px;color:#66656a;font-size:11.5px;line-height:1.8;text-align:center}
#toast{position:fixed;left:50%;bottom:40px;transform:translateX(-50%);background:#f2f1ed;color:#0d0f13;padding:10px 18px;border-radius:12px;font-size:14px;opacity:0;transition:.25s}
#wx{display:none;position:fixed;inset:0;background:rgba(0,0,0,.88);color:#fff;font-size:17px;line-height:1.8;text-align:right;padding:24px 28px}
</style></head><body>
<div class="notice"><span>⚠️</span><div><b>内部测试应用，不开放公众使用。</b><br>本页面及安装包仅供受邀的内部测试人员使用，请勿转发或公开传播。</div></div>
<div class="card">
 <svg class="mark" viewBox="0 0 100 100"><rect width="100" height="100" rx="27" fill="#15171c" stroke="rgba(255,255,255,.12)"/>
  <path d="M 74.80 46.87 A 25 25 0 1 1 53.13 25.20" fill="none" stroke="#fff" stroke-width="9.5" stroke-linecap="round"/>
  <circle cx="67.68" cy="32.32" r="7.5" fill="#12a150"/></svg>
 <h1>{{.App}}<span class="tag">内测版</span></h1>
 <div class="slogan">{{.Slogan}}</div>
 {{if .Code}}<div class="gift"><small>好友邀请码</small><div class="code">{{.Code}}</div>
  {{if .InviteeDays}}<p>安装后首次打开自动领取 {{.InviteeDays}} 天会员</p>{{end}}</div>{{end}}

 <div class="pkg">
  <h3>Android{{with .Android}}{{if .Version}}<span>v{{.Version}} ({{.Build}})</span>{{end}}{{end}}</h3>
  {{with .Android}}<div class="meta">{{if .Date}}更新于 {{.Date}}{{end}}{{if .Size}} · {{.Size}}{{end}}</div>
   {{if .Notes}}<ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul>{{end}}
   <a class="btn primary" href="{{.URL}}" onclick="cp()">下载 Android 安装包</a>
   {{range .Others}}<a class="alt" href="{{.URL}}" onclick="cp()">下载 {{.Label}}{{if .Size}} · {{.Size}}{{end}}</a>{{end}}
  {{else}}<a class="btn primary disabled">暂未提供</a>{{end}}
 </div>
 <div class="pkg">
  <h3>iPhone{{with .IOS}}{{if .Version}}<span>v{{.Version}} ({{.Build}})</span>{{end}}{{end}}</h3>
  {{with .IOS}}<div class="meta">{{if .Date}}更新于 {{.Date}}{{end}}{{if .Size}} · {{.Size}}{{end}}</div>
   {{if .Notes}}<ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul>{{end}}
   <a class="btn ghost" href="{{.URL}}" onclick="cp()">安装 iPhone 版</a>
   <div class="tip">请用 Safari 打开本页安装；仅已登记设备的测试人员可以安装。安装后如提示「未受信任的开发者」，到 设置 → 通用 → VPN 与设备管理 中信任。</div>
  {{else}}<a class="btn ghost disabled">iPhone 版暂未提供</a>{{end}}
 </div>
 {{with .Windows}}<div class="pkg">
  <h3>Windows 电脑{{if .Version}}<span>v{{.Version}} ({{.Build}})</span>{{end}}</h3>
  <div class="meta">{{if .Date}}更新于 {{.Date}}{{end}}{{if .Size}} · {{.Size}}{{end}}</div>
  {{if .Notes}}<ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul>{{end}}
  <a class="btn ghost" href="{{.URL}}" onclick="cp()">下载 Windows {{or .Label "版"}}</a>
  {{range .Others}}<a class="alt" href="{{.URL}}" onclick="cp()">下载 {{.Label}}{{if .Size}} · {{.Size}}{{end}}</a>{{end}}
  <div class="tip">支持 Windows 10 / 11（64 位）。下载后解压，打开文件夹里的 Huanyou.exe；请勿只单独拷出 exe。连接后通过系统代理生效，浏览器和大多数软件无需设置。</div>
 </div>{{end}}
 {{with .Mac}}<div class="pkg">
  <h3>Mac 电脑{{if .Version}}<span>v{{.Version}} ({{.Build}})</span>{{end}}</h3>
  <div class="meta">{{if .Date}}更新于 {{.Date}}{{end}}{{if .Size}} · {{.Size}}{{end}}</div>
  {{if .Notes}}<ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul>{{end}}
  <a class="btn ghost" href="{{.URL}}" onclick="cp()">下载 Mac {{or .Label "版"}}</a>
  {{range .Others}}<a class="alt" href="{{.URL}}" onclick="cp()">下载 {{.Label}}{{if .Size}} · {{.Size}}{{end}}</a>{{end}}
  <div class="tip">支持 macOS 12 及以上（Apple 芯片和 Intel）。下载后双击打开，把「环游」拖进「应用程序」文件夹。首次打开如提示无法验证开发者，请右键点 App 选「打开」。需要使用管理员账户，连接后通过系统代理生效。</div>
 </div>{{end}}
 <div class="tip">Android：下载完成后点击安装包安装；如被拦截，请在系统设置里允许「安装未知应用」。{{if .Code}}安装前请保持邀请码已复制，首次打开会自动识别。{{end}}</div>
</div>
<footer>{{.App}} 内部测试版 · 仅限受邀测试人员使用，不对外提供服务<br>© {{.Year}} {{.App}} 测试团队</footer>
<div id="toast">邀请码已复制</div>
<div id="wx">点右上角 ··· <br>选择「在浏览器打开」<br>即可下载 ↗</div>
<script>
var code={{.Code}},app={{.App}};
function cp(){if(!code)return;var t=app+"邀请码 "+code;
 try{navigator.clipboard.writeText(t)}catch(e){var a=document.createElement("textarea");a.value=t;document.body.appendChild(a);a.select();document.execCommand("copy");a.remove()}
 var el=document.getElementById("toast");el.style.opacity=1;setTimeout(function(){el.style.opacity=0},1600)}
if(/MicroMessenger|QQ\//i.test(navigator.userAgent))document.getElementById("wx").style.display="block";
</script></body></html>`))
