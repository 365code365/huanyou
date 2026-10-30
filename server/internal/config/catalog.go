package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Catalog is the part of the API configuration that operators edit at runtime
// from the admin panel: plans, nodes, recommended apps and feature settings.
//
// On first start it is seeded from api.json; afterwards the copy stored in the
// database is authoritative (see store.LoadCatalog / SaveCatalog). Every change
// goes through Normalize, so the admin panel and the JSON file share one set
// of validation rules.
type Catalog struct {
	// TrafficPeriodDays: traffic quotas reset every N days (default 30).
	TrafficPeriodDays int `json:"traffic_period_days"`
	// FreePlan is the plan id users get without an active paid plan ("" = no free tier).
	FreePlan string         `json:"free_plan"`
	Plans    []Plan         `json:"plans"`
	Nodes    []Node         `json:"nodes"`
	Apps     []AppRec       `json:"apps"`
	Referral ReferralConfig `json:"referral"`
	Monitor  MonitorConfig  `json:"monitor"`
	// Contacts are customer-service and social channels shown in the app.
	Contacts    []Contact         `json:"contacts"`
	Share       ShareConfig       `json:"share"`
	MultiDevice MultiDeviceConfig `json:"multi_device"`
	// AIDomains are routed through the proxy in "ai" mode (defaults applied if empty).
	AIDomains []string `json:"ai_domains"`
	// Risk: IP records and abuse thresholds.
	Risk RiskConfig `json:"risk"`
	// Segments: thresholds for the user segmentation report.
	Segments SegmentConfig `json:"segments"`
}

// RiskConfig controls IP recording and the suspicious-behaviour lists.
type RiskConfig struct {
	// RecordAPI / RecordNode: keep the addresses users call the API from and
	// connect to nodes from. Pointers so "unset" (old catalogs) means on.
	RecordAPI  *bool `json:"record_api"`
	RecordNode *bool `json:"record_node"`
	// RetentionDays: IP records older than this are deleted (default 30).
	RetentionDays int `json:"retention_days"`
	// MaxIPsPerUser: an account seen from more distinct IPs than this within
	// 24 h is listed as suspicious (default 8; mobile networks change IPs often).
	MaxIPsPerUser int `json:"max_ips_per_user"`
	// MaxUsersPerIP: an IP used by more subscriptions than this within 24 h is
	// listed as suspicious (default 4; multi-device members count as their owner).
	MaxUsersPerIP int `json:"max_users_per_ip"`
}

func (r RiskConfig) APIOn() bool  { return r.RecordAPI == nil || *r.RecordAPI }
func (r RiskConfig) NodeOn() bool { return r.RecordNode == nil || *r.RecordNode }

// SegmentConfig holds the thresholds used to classify users.
type SegmentConfig struct {
	HighValueCents  int `json:"high_value_cents"` // lifetime spend at or above = 高价 (default ¥100)
	LowValueCents   int `json:"low_value_cents"`  // paid but below = 低价 (default ¥30); in between = 中价
	LoyalOrders     int `json:"loyal_orders"`     // paid orders for 优质 (default 2, i.e. repurchased)
	ActiveDays      int `json:"active_days"`      // days with traffic in 14 days to count as active (default 4)
	PromoterInvites int `json:"promoter_invites"` // invites for 推广达人 (default 3)
	ChurnDays       int `json:"churn_days"`       // paid plan expired more than N days ago = 已流失 (default 14)
}

// RealityPublic is the client-side half of a node's REALITY settings.
type RealityPublic struct {
	PublicKey   string `json:"public_key"`
	ShortID     string `json:"short_id"`
	ServerName  string `json:"server_name"` // SNI shown on the wire, must be in the node's server_names
	Fingerprint string `json:"fingerprint"` // uTLS fingerprint, default "chrome"
}

// Node is a proxy node as the API sees it.
type Node struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Region  string `json:"region"`  // ISO country code, e.g. "US" (drives the flag in the app)
	Address string `json:"address"` // public IP or domain the app dials
	// Token authenticates the node agent when it pulls users / reports traffic. Secret.
	Token   string        `json:"token"`
	Reality RealityPublic `json:"reality"`
	// RealityPrivateKey is kept so the admin panel can show the node's install
	// config. Never sent to app clients.
	RealityPrivateKey string `json:"reality_private_key,omitempty"`
	// RealityTarget is the camouflage site for the node's install config, e.g. "www.apple.com:443".
	RealityTarget string `json:"reality_target,omitempty"`
	// VisionPort: public port (443). Serves VLESS + XTLS-Vision + REALITY.
	VisionPort int `json:"vision_port"`
	// XHTTPPath: if set, the same port also serves VLESS + XHTTP + REALITY.
	XHTTPPath string `json:"xhttp_path"`
	// Disabled hides the node from the app and from cluster mode (下架).
	Disabled bool `json:"disabled"`
	// Frozen (冻结) stops the node serving anyone: hidden from the app and the
	// agent receives an empty user list, so existing sessions are dropped.
	Frozen       bool   `json:"frozen"`
	FrozenReason string `json:"frozen_reason"`
	// Note is an internal admin remark (provider, expiry date, cost …).
	Note string `json:"note"`
	// Tags group nodes for search / filtering, e.g. ["高速", "流媒体"].
	Tags []string `json:"tags"`
	// Rate (流量倍率) multiplies traffic billed on this node (1 = normal, 0.5 = half).
	Rate float64 `json:"rate"`
	// Plans restricts the node to these plan ids ("" / empty = every plan).
	Plans []string `json:"plans"`
	// BandwidthMbps is the node's usable uplink. When set, the agent shares it
	// fairly between active users under load (see node.FairShare). 0 = off.
	BandwidthMbps int `json:"bandwidth_mbps"`
	// Weight biases cluster-mode ordering (higher first, default 1).
	Weight int `json:"weight"`
	// Sort orders the node list in the app (ascending).
	Sort int `json:"sort"`
}

// Serving reports whether the node is offered to users at all.
func (n *Node) Serving() bool { return !n.Disabled && !n.Frozen }

// AllowsPlan reports whether users on planID may use the node.
func (n *Node) AllowsPlan(planID string) bool {
	if len(n.Plans) == 0 {
		return true
	}
	for _, p := range n.Plans {
		if p == planID {
			return true
		}
	}
	return false
}

// Plan is a subscription tier. The free plan has price 0 and duration 0.
type Plan struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	PriceCents   int64   `json:"price_cents"`   // CNY fen; 2500 = ¥25.00
	DurationDays int     `json:"duration_days"` // validity of one purchase
	TrafficGB    float64 `json:"traffic_gb"`    // quota per traffic period (up+down), 0 = unlimited
	SpeedMbps    int     `json:"speed_mbps"`    // 0 = unlimited
	// OverQuotaKbps: speed once the quota is used up. 0 = disconnect instead.
	OverQuotaKbps int  `json:"over_quota_kbps"`
	Hidden        bool `json:"hidden"` // not offered for sale (existing subscribers keep it)
	Sort          int  `json:"sort"`
}

func (p *Plan) QuotaBytes() int64 { return int64(p.TrafficGB * (1 << 30)) }

// ReferralConfig grants subscription days for successful invitations.
type ReferralConfig struct {
	Enabled      bool   `json:"enabled"`
	RewardPlanID string `json:"reward_plan_id"` // paid plan the bonus days apply to
	InviterDays  int    `json:"inviter_days"`   // days to the inviter per accepted invite
	InviteeDays  int    `json:"invitee_days"`   // days to the new user who entered a code
	MaxInvites   int    `json:"max_invites"`    // cap rewarded invites per inviter (0 = unlimited)
}

// MonitorConfig controls node health checks and alerting.
type MonitorConfig struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"interval_seconds"` // default 60
	TimeoutSeconds  int  `json:"timeout_seconds"`  // default 8
	FailThreshold   int  `json:"fail_threshold"`   // consecutive failures before "down" (default 3)
	// AlertWebhook receives a POST on status changes.
	AlertWebhook string `json:"alert_webhook"`
	// AlertFormat: "generic" ({"text"}; Slack / 自建), "feishu", "dingtalk", "wecom".
	AlertFormat string `json:"alert_format"`
}

// Contact is a customer-service or social channel shown in the app's "我的" page.
type Contact struct {
	ID    string `json:"id"`
	Type  string `json:"type"`  // telegram | x | instagram | facebook | youtube | tiktok | discord | whatsapp | wechat | qq | email | website
	Group string `json:"group"` // "service" (客服) | "social" (关注我们)
	Name  string `json:"name"`  // label, e.g. "Telegram 客服"
	// Value is the handle / account / address, e.g. "orbit_support", "@orbitvpn", "help@x.com".
	Value string `json:"value"`
	// URL overrides the link derived from Type + Value (required for discord / website).
	URL      string `json:"url"`
	Desc     string `json:"desc"`
	Disabled bool   `json:"disabled"`
	Sort     int    `json:"sort"`
}

// ContactTypes lists supported contact types with their Chinese labels.
var ContactTypes = map[string]string{
	"telegram": "Telegram", "x": "X (Twitter)", "instagram": "Instagram", "facebook": "Facebook",
	"youtube": "YouTube", "tiktok": "TikTok", "discord": "Discord", "whatsapp": "WhatsApp",
	"wechat": "微信", "qq": "QQ", "email": "邮箱", "website": "网站",
}

// Link returns the URL the app opens for this contact ("" = copy Value instead).
func (c *Contact) Link() string {
	if c.URL != "" {
		return c.URL
	}
	h := strings.TrimPrefix(strings.TrimSpace(c.Value), "@")
	if h == "" {
		return ""
	}
	switch c.Type {
	case "telegram":
		return "https://t.me/" + h
	case "x":
		return "https://x.com/" + h
	case "instagram":
		return "https://www.instagram.com/" + h
	case "facebook":
		return "https://www.facebook.com/" + h
	case "youtube":
		return "https://www.youtube.com/@" + h
	case "tiktok":
		return "https://www.tiktok.com/@" + h
	case "whatsapp":
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, h)
		return "https://wa.me/" + digits
	case "email":
		return "mailto:" + h
	case "website":
		if strings.HasPrefix(h, "http") {
			return h
		}
		return "https://" + h
	}
	return "" // wechat / qq / discord without URL: the app copies the value
}

// ShareConfig drives the invite landing page and download links.
type ShareConfig struct {
	// AndroidURL / IOSURL are where the landing page sends users. If AndroidURL
	// is empty and the API hosts an APK in download_dir, that file is used.
	AndroidURL string `json:"android_url"`
	IOSURL     string `json:"ios_url"`
	Slogan     string `json:"slogan"` // shown on the landing page and share poster
}

// MultiDeviceConfig controls sharing one subscription across devices.
type MultiDeviceConfig struct {
	// MaxDevices is the total devices per subscription, including the owner (default 5).
	MaxDevices int `json:"max_devices"`
	// MinPlanDays: only paid plans with at least this duration may share (default 30 = 月度会员及以上).
	MinPlanDays int `json:"min_plan_days"`
}

// AppRec is a recommended app shown inside the VPN app.
type AppRec struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IconURL  string `json:"icon_url"`
	URL      string `json:"url"` // store link / download page / deep link
	Category string `json:"category"`
	Desc     string `json:"desc"`
	Disabled bool   `json:"disabled"`
	Sort     int    `json:"sort"`
}

// DefaultAIDomains are proxied in "ai" mode when ai_domains is empty.
var DefaultAIDomains = []string{
	"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com",
	"anthropic.com", "claude.ai", "gemini.google.com", "bard.google.com",
	"generativelanguage.googleapis.com", "aistudio.google.com",
	"x.ai", "grok.com", "perplexity.ai", "poe.com", "midjourney.com",
	"huggingface.co", "copilot.microsoft.com", "githubcopilot.com",
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Normalize fills defaults and validates the catalog. Safe to call repeatedly.
func (c *Catalog) Normalize() error {
	if c.TrafficPeriodDays <= 0 {
		c.TrafficPeriodDays = 30
	}
	if c.Monitor.IntervalSeconds <= 0 {
		c.Monitor.IntervalSeconds = 60
	}
	if c.Monitor.TimeoutSeconds <= 0 {
		c.Monitor.TimeoutSeconds = 8
	}
	if c.Monitor.FailThreshold <= 0 {
		c.Monitor.FailThreshold = 3
	}
	if c.Monitor.AlertWebhook != "" && !strings.HasPrefix(c.Monitor.AlertWebhook, "http") {
		return errors.New("预警 Webhook 必须是 http(s) 地址")
	}
	switch c.Monitor.AlertFormat {
	case "":
		c.Monitor.AlertFormat = "generic"
	case "generic", "feishu", "dingtalk", "wecom":
	default:
		return errors.New("预警格式只能是 generic / feishu / dingtalk / wecom")
	}
	if len(c.AIDomains) == 0 {
		c.AIDomains = append([]string(nil), DefaultAIDomains...)
	}
	r := &c.Risk
	if r.RetentionDays <= 0 {
		r.RetentionDays = 30
	}
	r.RetentionDays = min(r.RetentionDays, 365)
	if r.MaxIPsPerUser <= 0 {
		r.MaxIPsPerUser = 8
	}
	if r.MaxUsersPerIP <= 0 {
		r.MaxUsersPerIP = 4
	}
	g := &c.Segments
	if g.HighValueCents <= 0 {
		g.HighValueCents = 10000
	}
	if g.LowValueCents <= 0 {
		g.LowValueCents = 3000
	}
	if g.LowValueCents >= g.HighValueCents {
		return errors.New("分群设置：低价门槛必须小于高价门槛")
	}
	if g.LoyalOrders <= 0 {
		g.LoyalOrders = 2
	}
	if g.ActiveDays <= 0 {
		g.ActiveDays = 4
	}
	g.ActiveDays = min(g.ActiveDays, 14)
	if g.PromoterInvites <= 0 {
		g.PromoterInvites = 3
	}
	if g.ChurnDays <= 0 {
		g.ChurnDays = 14
	}
	for i, d := range c.AIDomains {
		c.AIDomains[i] = strings.ToLower(strings.TrimSpace(d))
	}
	if c.Plans == nil {
		c.Plans = []Plan{}
	}
	if c.Nodes == nil {
		c.Nodes = []Node{}
	}
	if c.Apps == nil {
		c.Apps = []AppRec{}
	}
	if c.Contacts == nil {
		c.Contacts = []Contact{}
	}
	if c.MultiDevice.MaxDevices <= 0 {
		c.MultiDevice.MaxDevices = 5
	}
	if c.MultiDevice.MaxDevices > 20 {
		return errors.New("每个订阅最多 20 台设备")
	}
	if c.MultiDevice.MinPlanDays <= 0 {
		c.MultiDevice.MinPlanDays = 30
	}
	if err := c.validateContacts(); err != nil {
		return err
	}
	for _, u := range []string{c.Share.AndroidURL, c.Share.IOSURL} {
		if u != "" && !strings.HasPrefix(u, "http") {
			return errors.New("下载链接必须是 http(s) 地址")
		}
	}
	if err := c.validatePlans(); err != nil {
		return err
	}
	if err := c.validateNodes(); err != nil {
		return err
	}
	if err := c.validateApps(); err != nil {
		return err
	}
	if r := &c.Referral; r.Enabled {
		p := c.Plan(r.RewardPlanID)
		if p == nil || p.ID == c.FreePlan {
			return errors.New("邀请奖励必须指定一个付费套餐")
		}
		if r.InviterDays <= 0 && r.InviteeDays <= 0 {
			return errors.New("邀请奖励天数至少设置一项")
		}
	}
	if c.Referral.InviterDays < 0 || c.Referral.InviteeDays < 0 || c.Referral.MaxInvites < 0 {
		return errors.New("邀请奖励数值不能为负")
	}
	return nil
}

func (c *Catalog) validatePlans() error {
	seen := map[string]bool{}
	for _, p := range c.Plans {
		switch {
		case !idPattern.MatchString(p.ID):
			return fmt.Errorf("套餐 ID %q 只能包含小写字母、数字、- 和 _", p.ID)
		case strings.TrimSpace(p.Name) == "":
			return fmt.Errorf("套餐 %s 缺少名称", p.ID)
		case seen[p.ID]:
			return fmt.Errorf("套餐 ID %q 重复", p.ID)
		case p.PriceCents < 0 || p.TrafficGB < 0 || p.SpeedMbps < 0 || p.OverQuotaKbps < 0 || p.DurationDays < 0:
			return fmt.Errorf("套餐 %s 的数值不能为负", p.ID)
		case p.ID != c.FreePlan && (p.PriceCents == 0 || p.DurationDays <= 0):
			return fmt.Errorf("付费套餐 %s 需要设置价格和有效天数", p.ID)
		}
		seen[p.ID] = true
	}
	if c.FreePlan != "" && !seen[c.FreePlan] {
		return fmt.Errorf("免费套餐 %q 不存在", c.FreePlan)
	}
	return nil
}

func (c *Catalog) validateNodes() error {
	seen := map[string]bool{}
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if n.Weight <= 0 {
			n.Weight = 1
		}
		if n.VisionPort == 0 {
			n.VisionPort = 443
		}
		if n.Reality.Fingerprint == "" {
			n.Reality.Fingerprint = "chrome"
		}
		switch {
		case !idPattern.MatchString(n.ID):
			return fmt.Errorf("节点 ID %q 只能包含小写字母、数字、- 和 _", n.ID)
		case seen[n.ID]:
			return fmt.Errorf("节点 ID %q 重复", n.ID)
		case strings.TrimSpace(n.Name) == "" || strings.TrimSpace(n.Address) == "":
			return fmt.Errorf("节点 %s 缺少名称或地址", n.ID)
		case n.VisionPort < 1 || n.VisionPort > 65535:
			return fmt.Errorf("节点 %s 端口无效", n.ID)
		case len(n.Token) < 24:
			return fmt.Errorf("节点 %s 的 token 至少 24 位", n.ID)
		case n.Reality.PublicKey == "" || n.Reality.ServerName == "" || n.Reality.ShortID == "":
			return fmt.Errorf("节点 %s 缺少 REALITY 公钥 / SNI / short id", n.ID)
		case n.XHTTPPath != "" && !strings.HasPrefix(n.XHTTPPath, "/"):
			return fmt.Errorf("节点 %s 的 xhttp_path 必须以 / 开头", n.ID)
		}
		n.Region = strings.ToUpper(strings.TrimSpace(n.Region))
		if n.Rate == 0 {
			n.Rate = 1
		}
		if n.Rate < 0 || n.Rate > 10 {
			return fmt.Errorf("节点 %s 的流量倍率需在 0–10 之间", n.ID)
		}
		if n.BandwidthMbps < 0 || n.BandwidthMbps > 100000 {
			return fmt.Errorf("节点 %s 的带宽数值无效", n.ID)
		}
		n.Note = strings.TrimSpace(n.Note)
		n.FrozenReason = strings.TrimSpace(n.FrozenReason)
		tags := []string{}
		for _, t := range n.Tags {
			if t = strings.TrimSpace(t); t != "" && !slicesContains(tags, t) {
				tags = append(tags, t)
			}
		}
		n.Tags = tags
		if n.Plans == nil {
			n.Plans = []string{}
		}
		for _, p := range n.Plans {
			if c.Plan(p) == nil {
				return fmt.Errorf("节点 %s 限定的套餐 %q 不存在", n.ID, p)
			}
		}
		seen[n.ID] = true
	}
	return nil
}

func (c *Catalog) validateApps() error {
	seen := map[string]bool{}
	for _, a := range c.Apps {
		switch {
		case !idPattern.MatchString(a.ID):
			return fmt.Errorf("应用 ID %q 只能包含小写字母、数字、- 和 _", a.ID)
		case seen[a.ID]:
			return fmt.Errorf("应用 ID %q 重复", a.ID)
		case strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.URL) == "":
			return fmt.Errorf("应用 %s 缺少名称或链接", a.ID)
		}
		seen[a.ID] = true
	}
	return nil
}

func (c *Catalog) validateContacts() error {
	seen := map[string]bool{}
	for i := range c.Contacts {
		k := &c.Contacts[i]
		k.ID, k.Name, k.Value, k.URL = strings.TrimSpace(k.ID), strings.TrimSpace(k.Name), strings.TrimSpace(k.Value), strings.TrimSpace(k.URL)
		if k.Group == "" {
			k.Group = "service"
		}
		switch {
		case !idPattern.MatchString(k.ID):
			return fmt.Errorf("联系方式 ID %q 只能包含小写字母、数字、- 和 _", k.ID)
		case seen[k.ID]:
			return fmt.Errorf("联系方式 ID %q 重复", k.ID)
		case ContactTypes[k.Type] == "":
			return fmt.Errorf("联系方式 %s 的类型 %q 不支持", k.ID, k.Type)
		case k.Group != "service" && k.Group != "social":
			return fmt.Errorf("联系方式 %s 的分组只能是 service 或 social", k.ID)
		case k.Value == "" && k.URL == "":
			return fmt.Errorf("联系方式 %s 需要填写账号或链接", k.ID)
		case k.URL != "" && !strings.HasPrefix(k.URL, "http") && !strings.HasPrefix(k.URL, "mailto:") && !strings.HasPrefix(k.URL, "tg:"):
			return fmt.Errorf("联系方式 %s 的链接格式不正确", k.ID)
		}
		if k.Name == "" {
			k.Name = ContactTypes[k.Type]
		}
		seen[k.ID] = true
	}
	return nil
}

// Clone returns a deep copy, so an admin edit can be validated before it replaces the live catalog.
func (c *Catalog) Clone() *Catalog {
	b, _ := json.Marshal(c)
	var out Catalog
	_ = json.Unmarshal(b, &out)
	return &out
}

// PlansByPrice returns a copy of the plans sorted by price ascending (free
// first), then by duration, then by the manual sort value.
func (c *Catalog) PlansByPrice() []Plan {
	list := append([]Plan(nil), c.Plans...)
	sortSlice(list, func(a, b Plan) bool {
		if a.PriceCents != b.PriceCents {
			return a.PriceCents < b.PriceCents
		}
		if a.DurationDays != b.DurationDays {
			return a.DurationDays < b.DurationDays
		}
		return a.Sort < b.Sort
	})
	return list
}

func (c *Catalog) Plan(id string) *Plan {
	for i := range c.Plans {
		if c.Plans[i].ID == id {
			return &c.Plans[i]
		}
	}
	return nil
}

func (c *Catalog) NodeByID(id string) *Node {
	for i := range c.Nodes {
		if c.Nodes[i].ID == id {
			return &c.Nodes[i]
		}
	}
	return nil
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// EnabledNodes returns nodes offered to clients (not disabled, not frozen),
// sorted by Sort then Weight (desc).
func (c *Catalog) EnabledNodes() []*Node { return c.NodesForPlan("") }

// NodesForPlan is EnabledNodes restricted to nodes that allow planID
// ("" = no restriction check).
func (c *Catalog) NodesForPlan(planID string) []*Node {
	var out []*Node
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if n.Serving() && (planID == "" || n.AllowsPlan(planID)) {
			out = append(out, n)
		}
	}
	sortSlice(out, func(a, b *Node) bool {
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		return a.Weight > b.Weight
	})
	return out
}

// NodeByToken returns the node whose agent token matches (constant-time compare per node).
func (c *Catalog) NodeByToken(tok string, eq func(a, b string) bool) *Node {
	var found *Node
	for i := range c.Nodes {
		if eq(c.Nodes[i].Token, tok) {
			found = &c.Nodes[i]
		}
	}
	return found
}
