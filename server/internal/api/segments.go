package api

import (
	"context"
	"sort"
	"sync"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/store"
)

// User segmentation for operations.
//
// Every user gets exactly one value tier (by lifetime spend) and any number of
// behaviour tags (loyalty, conversion potential, churn risk, …). Rules use the
// thresholds in catalog.segments and are computed from store.UserFacts (one
// aggregate query) every few minutes, not per request.

type segDef struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Group  string `json:"group"` // tier | tag
	Color  string `json:"color"`
	Desc   string `json:"desc"`   // how it is defined
	Advice string `json:"advice"` // suggested operation
}

var segDefs = []segDef{
	{"high", "高价客群", "tier", "gold", "累计消费 ≥ 高价门槛", "重点维护：专属客服、优先线路、年付续费礼遇，避免打扰式营销"},
	{"mid", "中价客群", "tier", "blue", "累计消费在低价与高价门槛之间", "推荐升级季付 / 年付，用「多付少算」引导提高客单价"},
	{"low", "低价客群", "tier", "cyan", "有付费，累计消费 < 低价门槛（多为周卡 / 首月）", "到期前推送月付 / 季付优惠，把一次性尝鲜转成连续订阅"},
	{"free", "未付费", "tier", "default", "从未付费的独立账号", "看「潜力客群」：活跃却没付费的用户最容易转化"},
	{"member", "共享成员", "tier", "purple", "通过多设备加入别人订阅的设备", "不单独营销，随主账号续费"},

	{"loyal", "优质客群", "tag", "green", "复购次数 ≥ 设定值，近 7 天仍在使用，且无风控记录", "老客回馈、邀请返利，鼓励带新；可邀请参加新功能内测"},
	{"potential", "潜力客群", "tag", "lime", "未付费，注册 ≥ 3 天，近 14 天活跃天数达标或近 30 天流量 ≥ 2 GB", "首单优惠 / 限时体验高速线路，转化成本最低的一批"},
	{"at_risk", "流失风险", "tag", "orange", "付费即将到期（3 天内）、到期未续（宽限期内），或付费中却 7 天未使用", "到期提醒 + 续费折扣；长期不用的先排查线路问题"},
	{"churned", "已流失", "tag", "red", "曾付费，到期超过流失天数仍未续费", "召回活动：回归礼包、新线路通知"},
	{"dormant", "沉睡用户", "tag", "default", "注册 > 7 天，近 30 天没有任何流量", "低成本触达（版本更新通知），不必投放优惠"},
	{"new", "新用户", "tag", "geekblue", "注册 7 天内", "新手引导，首单优惠，确保第一次连接成功"},
	{"promoter", "推广达人", "tag", "magenta", "邀请人数 ≥ 设定值", "提高邀请奖励、给专属邀请海报，做成推广渠道"},
	{"risky", "风险账号", "tag", "volcano", "被停用 / 封禁，或 24 小时内 IP 数超过风控阈值", "到「风控」页核查是否共享账号或倒卖，必要时封禁"},
}

var segByKey = func() map[string]segDef {
	m := map[string]segDef{}
	for _, d := range segDefs {
		m[d.Key] = d
	}
	return m
}()

// userSeg is the segmentation of one user.
type userSeg struct {
	Fact store.UserFact
	Tier string
	Tags []string
}

type segmentCache struct {
	mu     sync.Mutex
	at     time.Time
	byID   map[int64]*userSeg
	report *segReport
}

const segTTL = 3 * time.Minute

type segStat struct {
	segDef
	Users        int     `json:"users"`
	Share        float64 `json:"share"` // of all users
	RevenueCents int64   `json:"revenue_cents"`
	AvgLTVCents  int64   `json:"avg_ltv_cents"`
	PayingUsers  int     `json:"paying_users"`
	AvgBytes30   int64   `json:"avg_bytes_30"`
	ActiveShare  float64 `json:"active_share"` // share with traffic in the last 7 days
}

type planMix struct {
	PlanID       string `json:"plan_id"`
	Name         string `json:"name"`
	Orders       int64  `json:"orders"`
	RevenueCents int64  `json:"revenue_cents"`
	Buyers       int64  `json:"buyers"`
}

type segReport struct {
	GeneratedAt time.Time `json:"generated_at"`
	Totals      struct {
		Users          int     `json:"users"`
		Accounts       int     `json:"accounts"`        // excluding multi-device members
		Paying         int     `json:"paying"`          // ever paid
		PaidActive     int     `json:"paid_active"`     // paid plan valid now
		Conversion     float64 `json:"conversion"`      // paying / accounts
		RepurchaseRate float64 `json:"repurchase_rate"` // ≥2 paid orders / paying
		ARPPUCents     int64   `json:"arppu_cents"`     // lifetime revenue / paying
		RevenueCents   int64   `json:"revenue_cents"`
		Revenue30Cents int64   `json:"revenue_30_cents"`
		Active7        int     `json:"active_7"`
	} `json:"totals"`
	Tiers   []segStat            `json:"tiers"`
	Tags    []segStat            `json:"tags"`
	PlanMix []planMix            `json:"plan_mix"`
	Config  config.SegmentConfig `json:"config"`
}

// segments returns the cached segmentation, recomputing when stale.
func (s *Server) segments(ctx context.Context, force bool) (*segReport, map[int64]*userSeg, error) {
	c := &s.segCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.report != nil && time.Since(c.at) < segTTL {
		return c.report, c.byID, nil
	}
	rep, byID, err := s.computeSegments(ctx, time.Now())
	if err != nil {
		return nil, nil, err
	}
	c.report, c.byID, c.at = rep, byID, time.Now()
	return rep, byID, nil
}

func (s *Server) invalidateSegments() {
	s.segCache.mu.Lock()
	s.segCache.report = nil
	s.segCache.mu.Unlock()
}

func (s *Server) computeSegments(ctx context.Context, now time.Time) (*segReport, map[int64]*userSeg, error) {
	cat := s.cat()
	cfg := cat.Segments
	facts, err := s.store.UserFacts(ctx, now)
	if err != nil {
		return nil, nil, err
	}
	ipCounts, err := s.store.IPCounts(ctx, now.Add(-24*time.Hour).Unix())
	if err != nil {
		return nil, nil, err
	}
	revenue30, err := s.store.RevenueSince(ctx, now.AddDate(0, 0, -30).Unix())
	if err != nil {
		return nil, nil, err
	}
	mix, err := s.store.PlanMix(ctx)
	if err != nil {
		return nil, nil, err
	}
	s.riskFresh(ctx)
	blocked := s.blockedUserIDs()

	day := func(n int) string { return now.AddDate(0, 0, -n).Format("2006-01-02") }
	week, month := day(6), day(29)
	nowU := now.Unix()
	const dayS = 86400

	rep := &segReport{GeneratedAt: now, Config: cfg}
	byID := make(map[int64]*userSeg, len(facts))
	stats := map[string]*segStat{}
	for _, d := range segDefs {
		stats[d.Key] = &segStat{segDef: d}
	}
	active7 := map[string]int{}

	for _, f := range facts {
		us := &userSeg{Fact: f}
		member := f.PrimaryID != 0
		paidActive := f.PlanID != "" && f.ExpiresAt > nowU
		ageDays := (nowU - f.CreatedAt) / dayS
		recent7 := f.LastActiveDay != "" && f.LastActiveDay >= week
		recent30 := f.LastActiveDay != "" && f.LastActiveDay >= month

		switch {
		case member:
			us.Tier = "member"
		case f.LTVCents >= int64(cfg.HighValueCents):
			us.Tier = "high"
		case f.LTVCents >= int64(cfg.LowValueCents):
			us.Tier = "mid"
		case f.LTVCents > 0:
			us.Tier = "low"
		default:
			us.Tier = "free"
		}

		risky := f.Disabled || blocked[f.ID] || ipCounts[f.ID] > cat.Risk.MaxIPsPerUser
		tag := func(k string, cond bool) {
			if cond {
				us.Tags = append(us.Tags, k)
			}
		}
		tag("new", ageDays < 7)
		tag("risky", risky)
		tag("promoter", f.Invites >= cfg.PromoterInvites)
		if !member {
			everPaid := f.LTVCents > 0
			sinceExpiry := int64(0)
			if f.ExpiresAt > 0 && f.ExpiresAt <= nowU {
				sinceExpiry = (nowU - f.ExpiresAt) / dayS
			}
			churned := everPaid && !paidActive &&
				((f.ExpiresAt > 0 && sinceExpiry > int64(cfg.ChurnDays)) ||
					(f.ExpiresAt == 0 && nowU-f.LastPaidAt > int64(cfg.ChurnDays+30)*dayS))
			atRisk := everPaid && !churned && (
			// expiring within 3 days
			(paidActive && f.ExpiresAt-nowU <= 3*dayS) ||
				// expired, still within the grace period
				(!paidActive && f.ExpiresAt > 0) ||
				// paying but not using it
				(paidActive && !recent7 && ageDays >= 7))
			tag("loyal", f.PaidOrders >= cfg.LoyalOrders && (paidActive || nowU-f.LastPaidAt < 45*dayS) && recent7 && !risky)
			tag("potential", !everPaid && ageDays >= 3 && (f.ActiveDays14 >= cfg.ActiveDays || f.Bytes30 >= 2<<30))
			tag("at_risk", atRisk)
			tag("churned", churned)
			tag("dormant", ageDays >= 7 && !recent30 && !churned)
		}
		byID[f.ID] = us

		// Totals.
		t := &rep.Totals
		t.Users++
		if !member {
			t.Accounts++
			if f.LTVCents > 0 {
				t.Paying++
				t.RevenueCents += f.LTVCents
				if f.PaidOrders >= 2 {
					t.RepurchaseRate++
				}
			}
			if paidActive {
				t.PaidActive++
			}
		}
		if recent7 {
			t.Active7++
		}
		for _, k := range append([]string{us.Tier}, us.Tags...) {
			st := stats[k]
			st.Users++
			st.RevenueCents += f.LTVCents
			st.AvgBytes30 += f.Bytes30
			if f.LTVCents > 0 {
				st.PayingUsers++
			}
			if recent7 {
				active7[k]++
			}
		}
	}
	t := &rep.Totals
	if t.Paying > 0 {
		t.RepurchaseRate /= float64(t.Paying)
		t.ARPPUCents = t.RevenueCents / int64(t.Paying)
	}
	if t.Accounts > 0 {
		t.Conversion = float64(t.Paying) / float64(t.Accounts)
	}
	t.Revenue30Cents = revenue30
	for _, d := range segDefs {
		st := stats[d.Key]
		if st.Users > 0 {
			st.AvgBytes30 /= int64(st.Users)
			st.ActiveShare = float64(active7[d.Key]) / float64(st.Users)
			if t.Users > 0 {
				st.Share = float64(st.Users) / float64(t.Users)
			}
		}
		if st.PayingUsers > 0 {
			st.AvgLTVCents = st.RevenueCents / int64(st.PayingUsers)
		}
		if d.Group == "tier" {
			rep.Tiers = append(rep.Tiers, *st)
		} else {
			rep.Tags = append(rep.Tags, *st)
		}
	}
	for _, m := range mix {
		pm := planMix{PlanID: m.PlanID, Name: m.PlanID, Orders: m.Orders, RevenueCents: m.RevenueCents, Buyers: m.Buyers}
		if p := cat.Plan(m.PlanID); p != nil {
			pm.Name = p.Name
		}
		rep.PlanMix = append(rep.PlanMix, pm)
	}
	sort.Slice(rep.PlanMix, func(i, j int) bool { return rep.PlanMix[i].RevenueCents > rep.PlanMix[j].RevenueCents })
	return rep, byID, nil
}

// segOf returns tier + tags of one user from the cache ("" / nil if unknown).
func (s *Server) segOf(ctx context.Context, id int64) (string, []string) {
	_, byID, err := s.segments(ctx, false)
	if err != nil {
		return "", nil
	}
	if us := byID[id]; us != nil {
		return us.Tier, us.Tags
	}
	return "", nil
}

func hasSeg(us *userSeg, key string) bool {
	if us.Tier == key {
		return true
	}
	for _, t := range us.Tags {
		if t == key {
			return true
		}
	}
	return false
}
