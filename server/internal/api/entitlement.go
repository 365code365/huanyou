package api

import (
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/store"
)

// Entitlement is what a user may do right now, derived from plan + usage.
type Entitlement struct {
	PlanID     string     `json:"plan_id"`
	PlanName   string     `json:"plan_name"`
	Paid       bool       `json:"paid"`
	ExpiresAt  *time.Time `json:"expires_at"`  // paid plan expiry
	QuotaBytes int64      `json:"quota_bytes"` // 0 = unlimited
	UsedBytes  int64      `json:"used_bytes"`
	PeriodEnd  time.Time  `json:"period_end"` // when used_bytes resets
	SpeedKbps  int        `json:"speed_kbps"` // current limit, 0 = unlimited
	Throttled  bool       `json:"throttled"`  // quota used up, running at over_quota speed
	CanConnect bool       `json:"can_connect"`
	Reason     string     `json:"reason,omitempty"` // why CanConnect is false
}

func entitlementOf(cfg *config.Catalog, u *store.User, now time.Time) Entitlement {
	period := time.Duration(cfg.TrafficPeriodDays) * 24 * time.Hour
	e := Entitlement{UsedBytes: u.UsedInPeriod(now, period), PeriodEnd: u.PeriodEnd(now, period)}

	var plan *config.Plan
	switch {
	case u.PaidPlanActive(now):
		e.Paid, e.ExpiresAt, e.PlanID = true, u.ExpiresAt, u.PlanID
		plan = cfg.Plan(u.PlanID)
		if plan == nil {
			// Plan was removed from the config while the user still has time left:
			// keep serving them without limits rather than cutting off a paying user.
			plan = &config.Plan{ID: u.PlanID, Name: u.PlanID}
		}
	case cfg.FreePlan != "":
		plan = cfg.Plan(cfg.FreePlan)
		e.PlanID = plan.ID
	default:
		e.Reason = "请购买套餐"
		if u.PlanID != "" {
			e.Reason = "套餐已过期，请续费"
		}
	}
	if u.Disabled {
		e.CanConnect, e.Reason = false, "账号已被停用"
		if plan != nil {
			e.PlanName = plan.Name
		}
		return e
	}
	if plan == nil {
		return e
	}
	e.PlanName = plan.Name
	e.QuotaBytes = plan.QuotaBytes()
	e.SpeedKbps = plan.SpeedMbps * 1000
	e.CanConnect = true
	if e.QuotaBytes > 0 && e.UsedBytes >= e.QuotaBytes {
		if plan.OverQuotaKbps > 0 {
			e.Throttled = true
			e.SpeedKbps = plan.OverQuotaKbps
		} else {
			e.CanConnect = false
			e.Reason = "本周期流量已用完"
		}
	}
	return e
}
