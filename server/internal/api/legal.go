package api

import (
	"html/template"
	"net/http"
	"strings"
	"time"
)

// Built-in 用户协议 / 隐私政策 pages. Apple requires links to both on any
// subscription purchase screen (and a privacy policy URL in App Store
// Connect). The admin can point the app at their own pages instead
// (payment.terms_url / privacy_url).

// legalLinks are the URLs the purchase page links to.
func (s *Server) legalLinks() map[string]string {
	p := s.cat().Payment
	terms, privacy := strings.TrimSpace(p.TermsURL), strings.TrimSpace(p.PrivacyURL)
	if terms == "" {
		terms = s.cfg.PublicURL + "/legal/terms"
	}
	if privacy == "" {
		privacy = s.cfg.PublicURL + "/legal/privacy"
	}
	return map[string]string{"terms_url": terms, "privacy_url": privacy}
}

var legalTmpl = template.Must(template.New("legal").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} - {{.App}}</title>
<style>body{font-family:-apple-system,"PingFang SC",sans-serif;max-width:720px;margin:0 auto;padding:28px 22px 60px;color:#222;line-height:1.75;font-size:15px}
h1{font-size:24px;margin:0 0 4px}h2{font-size:17px;margin:26px 0 6px}.muted{color:#888;font-size:13px}ul{padding-left:20px}</style></head>
<body><h1>{{.Title}}</h1><div class="muted">{{.App}} · 更新于 {{.Date}}</div>{{.Body}}</body></html>`))

const termsBody = `
<h2>1. 服务内容</h2>
<p>{{APP}}（以下简称“本服务”）为用户提供加密网络连接服务，用于保护网络通信安全与隐私。请在遵守所在地法律法规的前提下使用本服务。</p>
<h2>2. 账号</h2>
<p>首次打开 App 会为本设备自动创建账号，无需注册。同一设备卸载重装后会自动找回原账号。您可以在「我的 → 多设备」中让其它设备共用同一订阅。</p>
<h2>3. 套餐与付费</h2>
<ul>
<li>付费套餐按时长提供服务，购买页会显示每个套餐的价格与有效天数，付款成功后立即生效。</li>
<li>通过 Apple App Store 购买时，款项由您的 Apple ID 账户支付；套餐<strong>到期不会自动续费</strong>，需要时可再次购买续期。同一套餐续费在原到期时间上顺延。</li>
<li>换手机后，可在购买页点击「恢复购买」，使用购买时的 Apple ID 恢复已购套餐；也可在原设备的「多设备」中生成令牌让新设备加入。</li>
<li>退款：通过 App Store 购买的，请按 Apple 的退款政策在 reportaproblem.apple.com 申请。</li>
</ul>
<h2>4. 使用规范</h2>
<p>不得利用本服务从事违法活动、攻击他人网络、发送垃圾信息或侵犯他人权利。违反者我们有权限制或终止服务。</p>
<h2>5. 服务变更与中断</h2>
<p>我们会尽力保证服务稳定，但因网络、维护或不可抗力造成的中断，我们将尽快恢复，并不承担由此产生的间接损失。</p>
<h2>6. 协议变更</h2>
<p>本协议更新后会在此页面公布，继续使用即视为同意更新后的协议。</p>
<h2>7. 联系我们</h2>
<p>如有疑问，请通过 App 内「我的 → 客服与社交」联系我们。</p>`

const privacyBody = `
<h2>我们收集的信息</h2>
<ul>
<li><strong>设备标识</strong>：用于自动创建和找回账号，不与您的真实身份关联。</li>
<li><strong>订阅与订单</strong>：套餐、到期时间、订单记录（不含银行卡或支付账号信息，支付由 Apple 或支付机构处理）。</li>
<li><strong>流量用量</strong>：仅统计使用的总流量，用于套餐额度计算。</li>
<li><strong>连接来源 IP</strong>：用于防止账号被盗用和滥用，保留 30 天后自动删除。</li>
<li><strong>崩溃日志</strong>：App 出错时的错误信息，用于修复问题，可在「我的 → 设置」中关闭。</li>
</ul>
<h2>我们不收集的信息</h2>
<p><strong>我们不记录您访问的网站、使用的应用、DNS 查询或通信内容。</strong>连接全程加密，服务器只负责转发。</p>
<h2>信息的使用与共享</h2>
<p>上述信息仅用于提供和改进本服务、防止滥用。除法律法规要求外，我们不会出售或向第三方提供您的个人信息。</p>
<h2>数据安全</h2>
<p>数据通过加密通道传输，存储在受访问控制保护的服务器上。</p>
<h2>您的权利</h2>
<p>您可以随时卸载 App 停止使用；如需删除账号及相关数据，请通过 App 内客服联系我们，我们将在核实后处理。</p>
<h2>政策更新</h2>
<p>本政策更新后会在此页面公布。</p>
<h2>联系我们</h2>
<p>如有疑问，请通过 App 内「我的 → 客服与社交」联系我们。</p>`

func (s *Server) handleLegal(w http.ResponseWriter, r *http.Request) {
	var title, body string
	switch r.PathValue("doc") {
	case "terms":
		title, body = "用户协议", termsBody
	case "privacy":
		title, body = "隐私政策", privacyBody
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_ = legalTmpl.Execute(w, map[string]any{
		"Title": title, "App": AppName, "Date": time.Now().Format("2006-01-02"),
		"Body": template.HTML(strings.ReplaceAll(body, "{{APP}}", AppName)),
	})
}
