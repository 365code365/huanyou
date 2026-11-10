package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"paycenter/internal/service"
	"paycenter/internal/store"
	"paycenter/internal/vpnproto"
	"paycenter/internal/xinlian"
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/login", s.loginPage)
	mux.HandleFunc("POST /admin/login", s.login)
	mux.HandleFunc("POST /admin/logout", s.requireAdmin("", s.logout))
	mux.HandleFunc("GET /admin/{$}", s.requireAdmin("dash", s.dashboard))

	mux.HandleFunc("GET /admin/orders", s.requireAdmin("orders", s.orders))
	mux.HandleFunc("GET /admin/orders/{id}", s.requireAdmin("orders", s.orderDetail))
	mux.HandleFunc("POST /admin/orders/{id}/sync", s.requireAdmin("orders", s.orderSync))
	mux.HandleFunc("POST /admin/orders/{id}/renotify", s.requireAdmin("orders", s.orderRenotify))

	mux.HandleFunc("GET /admin/tests", s.requireAdmin("tests", s.testsPage))
	mux.HandleFunc("POST /admin/tests", s.requireAdmin("tests", s.testCreate))
	mux.HandleFunc("POST /admin/tests/selftest", s.requireAdmin("tests", s.testSelf))
	mux.HandleFunc("GET /admin/tests/{id}", s.requireAdmin("tests", s.testDetail))
	mux.HandleFunc("GET /admin/tests/{id}/state", s.requireAdmin("tests", s.testState))
	mux.HandleFunc("POST /admin/tests/{id}/simulate", s.requireAdmin("tests", s.testSimulate))
	mux.HandleFunc("POST /admin/tests/{id}/query", s.requireAdmin("tests", s.testQuery))
	mux.HandleFunc("POST /admin/tests/{id}/renotify", s.requireAdmin("tests", s.testRenotify))

	mux.HandleFunc("GET /admin/channels", s.requireAdmin("channels", s.channels))
	mux.HandleFunc("GET /admin/channels/{id}", s.requireAdmin("channels", s.channelForm))
	mux.HandleFunc("POST /admin/channels/{id}", s.requireAdmin("channels", s.channelSave))
	mux.HandleFunc("POST /admin/channels/{id}/test", s.requireAdmin("channels", s.channelTest))
	mux.HandleFunc("POST /admin/channels/{id}/delete", s.requireAdmin("channels", s.channelDelete))

	mux.HandleFunc("GET /admin/merchants", s.requireAdmin("merchants", s.merchants))
	mux.HandleFunc("GET /admin/merchants/{id}", s.requireAdmin("merchants", s.merchantForm))
	mux.HandleFunc("POST /admin/merchants/{id}", s.requireAdmin("merchants", s.merchantSave))
	mux.HandleFunc("POST /admin/merchants/{id}/test-notify", s.requireAdmin("merchants", s.merchantTestNotify))
	mux.HandleFunc("POST /admin/merchants/{id}/delete", s.requireAdmin("merchants", s.merchantDelete))
	mux.HandleFunc("POST /admin/keys", s.requireAdmin("merchants", s.genKeys))

	mux.HandleFunc("GET /admin/settings", s.requireAdmin("settings", s.settings))
	mux.HandleFunc("POST /admin/settings", s.requireAdmin("settings", s.settingsSave))
	mux.HandleFunc("POST /admin/settings/password", s.requireAdmin("settings", s.passwordSave))
}

func back(w http.ResponseWriter, r *http.Request, path, key, msg string) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	http.Redirect(w, r, path+sep+key+"="+url.QueryEscape(msg), http.StatusSeeOther)
}

func pathID(r *http.Request) (uint, bool) {
	v := r.PathValue("id")
	if v == "new" {
		return 0, true
	}
	n, err := strconv.ParseUint(v, 10, 64)
	return uint(n), err == nil && n > 0
}

// qlink is the current query with some parameters replaced ("" = removed).
func qlink(path string, q url.Values, set map[string]string) string {
	v := url.Values{}
	for k := range q {
		if q.Get(k) != "" {
			v.Set(k, q.Get(k))
		}
	}
	for k, x := range set {
		if x == "" {
			v.Del(k)
		} else {
			v.Set(k, x)
		}
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

// ---- dashboard ----

type dayBar struct {
	Label string
	Cents int64
	Count int64
	Pct   int
}

type checkItem struct {
	Title, Desc, Link string
	Done              bool
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, c adminCtx) {
	db := s.Svc.DB
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	week := today.AddDate(0, 0, -6)
	real := func() *gorm.DB { return db.Model(&store.Order{}).Where("is_test = ?", false) }
	var st struct {
		TodayCents, WeekCents, TodayOrders, TodayPaid, NotifyPending, NotifyFailed, Duplicates int64
		Conv                                                                                   int64
	}
	real().Where("created_at >= ?", today).Count(&st.TodayOrders)
	real().Where("notify_status = ?", store.NotifyPending).Count(&st.NotifyPending)
	real().Where("notify_status = ?", store.NotifyFailed).Count(&st.NotifyFailed)
	db.Model(&store.OrderLog{}).Where("kind = ?", "duplicate").Distinct("order_id").Count(&st.Duplicates)

	bars := make([]dayBar, 7)
	for i := range bars {
		d := week.AddDate(0, 0, i)
		bars[i].Label = d.Format("01/02")
		if i == 6 {
			bars[i].Label = "今天"
		}
	}
	var paid []store.Order
	real().Select("id", "amount", "paid_at").Where("status = ? AND paid_at >= ?", store.OrderPaid, week).Find(&paid)
	for _, o := range paid {
		if o.PaidAt == nil {
			continue
		}
		p := o.PaidAt.In(time.Local)
		day := time.Date(p.Year(), p.Month(), p.Day(), 0, 0, 0, 0, time.Local)
		i := int(day.Sub(week).Hours()/24 + 0.5)
		if i < 0 || i > 6 {
			continue
		}
		bars[i].Cents += o.Amount
		bars[i].Count++
		st.WeekCents += o.Amount
		if i == 6 {
			st.TodayCents += o.Amount
			st.TodayPaid++
		}
	}
	var top int64
	for _, b := range bars {
		top = max(top, b.Cents)
	}
	for i := range bars {
		if top > 0 && bars[i].Cents > 0 {
			bars[i].Pct = max(int(bars[i].Cents*100/top), 3)
		}
	}
	if st.TodayOrders > 0 {
		st.Conv = st.TodayPaid * 100 / st.TodayOrders
	}

	var nChan, nMerch, nPassed, nRealPaid int64
	db.Model(&store.Channel{}).Where("enabled = ?", true).Count(&nChan)
	db.Model(&store.Merchant{}).Where("enabled = ?", true).Count(&nMerch)
	db.Model(&store.TestRun{}).Where("kind = ? AND passed = ?", "auto", true).Count(&nPassed)
	real().Where("status = ?", store.OrderPaid).Count(&nRealPaid)
	checks := []checkItem{
		{"设置对外地址", "信联pay 回调、VPN 对接都要用到", "/admin/settings", c.PublicURL != ""},
		{"添加支付通道", "填写信联pay 的商户号和密钥", "/admin/channels/new", nChan > 0},
		{"接入 VPN 商户", "填写 VPN 后台的商户号和两把密钥", "/admin/merchants/new", nMerch > 0},
		{"通过一次自检", "在「测试下单」里运行一键自检", "/admin/tests", nPassed > 0},
		{"收到第一笔真实支付", "在 VPN App 里下单并完成支付", "/admin/orders", nRealPaid > 0},
	}
	done := 0
	for _, it := range checks {
		if it.Done {
			done++
		}
	}
	var recent []store.Order
	real().Order("id DESC").Limit(8).Find(&recent)
	s.render(w, http.StatusOK, "dashboard", map[string]any{"C": c, "S": st, "Bars": bars, "Recent": recent,
		"Checks": checks, "Done": done, "DonePct": done * 100 / len(checks),
		"Today": now.Format("2006 年 1 月 2 日"), "PayLink": c.PublicURL + "/checkout", "QueryURL": c.PublicURL + "/api/order/query"})
}

// ---- orders ----

type tab struct {
	Name, Link string
	N          int64
	On         bool
}

func (s *Server) orders(w http.ResponseWriter, r *http.Request, c adminCtx) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	const size = 20
	base := func() *gorm.DB {
		db := s.Svc.DB.Model(&store.Order{})
		if v := strings.TrimSpace(q.Get("q")); v != "" {
			db = db.Where("out_trade_no = ? OR trade_no = ? OR upstream_trade_no = ?", v, v, v)
		}
		if v := q.Get("merchant"); v != "" {
			db = db.Where("merchant_id = ?", v)
		}
		switch q.Get("type") {
		case "real":
			db = db.Where("is_test = ?", false)
		case "test":
			db = db.Where("is_test = ?", true)
		}
		if t, err := time.ParseInLocation("2006-01-02", q.Get("from"), time.Local); err == nil {
			db = db.Where("created_at >= ?", t)
		}
		if t, err := time.ParseInLocation("2006-01-02", q.Get("to"), time.Local); err == nil {
			db = db.Where("created_at < ?", t.AddDate(0, 0, 1))
		}
		if q.Get("dup") == "1" {
			db = db.Where("id IN (?)", s.Svc.DB.Model(&store.OrderLog{}).Select("order_id").Where("kind = ?", "duplicate"))
		}
		return db
	}
	now := time.Now().Unix()
	byStatus := func(db *gorm.DB, st string) *gorm.DB {
		switch st {
		case "PAID":
			return db.Where("status = ?", store.OrderPaid)
		case "PENDING":
			return db.Where("status = ? AND expire_at >= ?", store.OrderPending, now)
		case "CLOSED":
			return db.Where("status = ? AND expire_at < ?", store.OrderPending, now)
		case "NOTIFY_FAILED":
			return db.Where("notify_status = ?", store.NotifyFailed)
		case "NOTIFY_PENDING":
			return db.Where("notify_status = ?", store.NotifyPending)
		}
		return db
	}
	status := q.Get("status")
	var tabs []tab
	for _, t := range [][2]string{{"", "全部"}, {"PENDING", "待支付"}, {"PAID", "已支付"}, {"CLOSED", "已过期"},
		{"NOTIFY_PENDING", "待通知"}, {"NOTIFY_FAILED", "通知失败"}} {
		var n int64
		byStatus(base(), t[0]).Count(&n)
		tabs = append(tabs, tab{Name: t[1], N: n, On: status == t[0], Link: qlink("/admin/orders", q, map[string]string{"status": t[0], "page": ""})})
	}
	var total int64
	byStatus(base(), status).Count(&total)
	var list []store.Order
	byStatus(base(), status).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&list)
	var merchants []store.Merchant
	s.Svc.DB.Order("id").Find(&merchants)
	pages := max(int((total+size-1)/size), 1)
	var prev, next string
	if page > 1 {
		prev = qlink("/admin/orders", q, map[string]string{"page": strconv.Itoa(page - 1)})
	}
	if page < pages {
		next = qlink("/admin/orders", q, map[string]string{"page": strconv.Itoa(page + 1)})
	}
	filtered := false
	for _, k := range []string{"q", "merchant", "type", "from", "to", "dup"} {
		if q.Get(k) != "" {
			filtered = true
		}
	}
	s.render(w, http.StatusOK, "orders", map[string]any{"C": c, "List": list, "Total": total, "Page": page,
		"Pages": pages, "Prev": prev, "Next": next, "Q": q.Get("q"), "Status": status, "Type": q.Get("type"),
		"From": q.Get("from"), "To": q.Get("to"), "Merchant": q.Get("merchant"), "Merchants": merchants,
		"Tabs": tabs, "Filtered": filtered, "Dup": q.Get("dup")})
}

func (s *Server) loadOrder(r *http.Request) (*store.Order, error) {
	id, ok := pathID(r)
	if !ok || id == 0 {
		return nil, errors.New("bad id")
	}
	var o store.Order
	if err := s.Svc.DB.Take(&o, id).Error; err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Server) orderDetail(w http.ResponseWriter, r *http.Request, c adminCtx) {
	o, err := s.loadOrder(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var attempts []store.Attempt
	s.Svc.DB.Where("order_id = ?", o.ID).Order("id DESC").Find(&attempts)
	var logs []store.OrderLog
	s.Svc.DB.Where("order_id = ?", o.ID).Order("id DESC").Limit(200).Find(&logs)
	var m store.Merchant
	s.Svc.DB.Where("merchant_id = ?", o.MerchantID).Take(&m)
	var run store.TestRun
	runID := uint(0)
	if o.IsTest && s.Svc.DB.Where("merchant_id = ? AND out_trade_no = ?", o.MerchantID, o.OutTradeNo).Take(&run).Error == nil {
		runID = run.ID
	}
	s.render(w, http.StatusOK, "order", map[string]any{"C": c, "O": o, "M": m, "Attempts": attempts, "Logs": logs,
		"RunID": runID, "PayLink": "/pay/" + o.TradeNo + "?t=" + s.Svc.PageToken(o.TradeNo)})
}

func (s *Server) orderSync(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	o, err := s.loadOrder(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := fmt.Sprintf("/admin/orders/%d", o.ID)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.Svc.SyncOrder(ctx, o); err != nil {
		back(w, r, path, "err", "查询上游失败："+err.Error())
		return
	}
	back(w, r, path, "ok", "已向上游查单并更新状态")
}

func (s *Server) orderRenotify(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	o, err := s.loadOrder(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := fmt.Sprintf("/admin/orders/%d", o.ID)
	if err := s.Svc.Renotify(o); err != nil {
		back(w, r, path, "err", err.Error())
		return
	}
	time.Sleep(1500 * time.Millisecond) // usually done by now; the page shows the result
	back(w, r, path, "ok", "已加入通知队列")
}

// ---- channels (信联pay accounts) ----

func (s *Server) channels(w http.ResponseWriter, r *http.Request, c adminCtx) {
	var list []store.Channel
	s.Svc.DB.Order("id").Find(&list)
	used := map[uint]int64{}
	var rows []struct {
		ChannelID uint
		N         int64
	}
	s.Svc.DB.Model(&store.Merchant{}).Select("channel_id, count(*) as n").Group("channel_id").Scan(&rows)
	for _, x := range rows {
		used[x.ChannelID] = x.N
	}
	s.render(w, http.StatusOK, "channels", map[string]any{"C": c, "List": list, "Used": used})
}

func (s *Server) channelForm(w http.ResponseWriter, r *http.Request, c adminCtx) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ch := store.Channel{Name: "信联pay", Provider: "xinlian", APIBase: "https://c02.z-cn.top", SignType: "MD5",
		PayTypes: store.EncodePayTypes([]store.PayType{{Code: "alipay", Name: "支付宝"}, {Code: "wxpay", Name: "微信支付"}}),
		Device:   "auto", Enabled: true}
	if id != 0 {
		if err := s.Svc.DB.Take(&ch, id).Error; err != nil {
			http.NotFound(w, r)
			return
		}
	}
	s.render(w, http.StatusOK, "channel", map[string]any{"C": c, "Ch": ch})
}

// payTypesFrom reads the pay type rows of the form (parallel arrays
// pt_code / pt_name / pt_state / pt_remark, one entry per row).
func payTypesFrom(r *http.Request) string {
	codes, names, states, remarks := r.PostForm["pt_code"], r.PostForm["pt_name"], r.PostForm["pt_state"], r.PostForm["pt_remark"]
	at := func(a []string, i int) string {
		if i < len(a) {
			return strings.Join(strings.Fields(a[i]), " ")
		}
		return ""
	}
	var list []store.PayType
	for i, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" || strings.ContainsAny(code, "=\n\t ") {
			continue
		}
		list = append(list, store.PayType{Code: code, Name: at(names, i), Hidden: at(states, i) == "hidden",
			Remark: store.Trunc(at(remarks, i), 200)})
	}
	return store.EncodePayTypes(list)
}

func (s *Server) channelSave(w http.ResponseWriter, r *http.Request, c adminCtx) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var ch store.Channel
	if id != 0 {
		if err := s.Svc.DB.Take(&ch, id).Error; err != nil {
			http.NotFound(w, r)
			return
		}
	}
	f := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	ch.Name, ch.Provider, ch.APIBase, ch.PID = f("name"), "xinlian", strings.TrimRight(f("api_base"), "/"), f("pid")
	ch.SignType, ch.Device, ch.PayTypes = strings.ToUpper(f("sign_type")), f("device"), payTypesFrom(r)
	ch.SignWithType, ch.Enabled = r.FormValue("sign_with_type") == "1", r.FormValue("enabled") == "1"
	if v := f("md5_key"); v != "" { // empty = keep
		ch.MD5Key = v
	}
	if v := f("rsa_private_key"); v != "" {
		ch.RSAPrivateKey = v
	}
	if r.FormValue("clear_rsa") == "1" {
		ch.RSAPrivateKey = ""
	}
	fail := func(msg string) {
		c.Err = msg
		s.render(w, http.StatusBadRequest, "channel", map[string]any{"C": c, "Ch": ch})
	}
	if ch.Name == "" {
		fail("请填写通道名称")
		return
	}
	if len(ch.PayTypeList()) == 0 {
		fail("请至少配置一种支付方式")
		return
	}
	if _, err := service.NewClient(&ch, c.PublicURL); err != nil {
		fail(err.Error())
		return
	}
	if err := s.Svc.DB.Save(&ch).Error; err != nil {
		fail("保存失败：" + err.Error())
		return
	}
	msg := "通道已保存"
	if len(ch.VisiblePayTypes()) == 0 {
		msg += "。注意：所有支付方式都已隐藏，使用这个通道的收银台暂时无法付款"
	}
	back(w, r, fmt.Sprintf("/admin/channels/%d", ch.ID), "ok", msg)
}

func (s *Server) channelTest(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	id, _ := pathID(r)
	var ch store.Channel
	if err := s.Svc.DB.Take(&ch, id).Error; err != nil {
		http.NotFound(w, r)
		return
	}
	path := fmt.Sprintf("/admin/channels/%d", ch.ID)
	cl, err := service.NewClient(&ch, s.Svc.PublicURL())
	if err != nil {
		back(w, r, path, "err", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	data, err := cl.MerchantInfo(ctx)
	if err != nil {
		back(w, r, path, "err", "连接失败："+err.Error())
		return
	}
	info := store.Trunc(compactJSON(data), 200)
	// merchant/info passes even when ordering is refused (e.g. 4484 domain
	// not authorized), so also place and immediately close a 0.01 order.
	// Prefer a shown method; if all are hidden, still test the first one.
	types := ch.VisiblePayTypes()
	if len(types) == 0 {
		types = ch.PayTypeList()
	}
	pub := s.Svc.PublicURL()
	if len(types) == 0 || pub == "" {
		back(w, r, path, "ok", "密钥正确（商户信息："+info+"）。未测试下单：需要先配置支付方式和对外地址")
		return
	}
	out := "PACHK" + time.Now().Format("20060102150405")
	res, err := cl.Submit(ctx, xinlian.SubmitRequest{Type: types[0].Code, OutTradeNo: out, Money: "0.01", Name: "通道连接测试",
		NotifyURL: fmt.Sprintf("%s/notify/xinlian/%d", pub, ch.ID), ReturnURL: pub + "/", Device: "pc"})
	if err != nil {
		back(w, r, path, "err", "密钥正确，但下单被拒绝："+err.Error())
		return
	}
	closeNote := "已立即关闭"
	if err := cl.Close(ctx, out); err != nil {
		closeNote = "关闭失败（" + err.Error() + "），未付款的订单会自动过期"
	}
	back(w, r, path, "ok", fmt.Sprintf("连接成功：密钥正确，%s 下单 0.01 元成功（受理类型 %s，上游单号 %s，%s）",
		types[0].Name, res.Type, res.PlatformTradeNo(), closeNote))
}

func compactJSON(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return string(b)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func (s *Server) channelDelete(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	id, _ := pathID(r)
	var n int64
	s.Svc.DB.Model(&store.Merchant{}).Where("channel_id = ?", id).Count(&n)
	if n > 0 {
		back(w, r, fmt.Sprintf("/admin/channels/%d", id), "err", "还有商户在使用这个通道，请先改绑")
		return
	}
	s.Svc.DB.Delete(&store.Channel{}, id)
	back(w, r, "/admin/channels", "ok", "通道已删除")
}

// ---- merchants (VPN systems) ----

func (s *Server) merchants(w http.ResponseWriter, r *http.Request, c adminCtx) {
	var list []store.Merchant
	s.Svc.DB.Order("id").Find(&list)
	var chans []store.Channel
	s.Svc.DB.Order("id").Find(&chans)
	names := map[uint]string{}
	for _, ch := range chans {
		names[ch.ID] = ch.Name
	}
	counts := map[string]int64{}
	var rows []struct {
		MerchantID string
		N          int64
	}
	s.Svc.DB.Model(&store.Order{}).Select("merchant_id, count(*) as n").Where("is_test = ?", false).Group("merchant_id").Scan(&rows)
	for _, x := range rows {
		counts[x.MerchantID] = x.N
	}
	s.render(w, http.StatusOK, "merchants", map[string]any{"C": c, "List": list, "ChannelNames": names, "Counts": counts})
}

func (s *Server) merchantData(c adminCtx, m store.Merchant) map[string]any {
	var chans []store.Channel
	s.Svc.DB.Order("id").Find(&chans)
	return map[string]any{"C": c, "M": m, "Channels": chans,
		"PayLink": c.PublicURL + "/checkout", "QueryURL": c.PublicURL + "/api/order/query"}
}

func (s *Server) merchantForm(w http.ResponseWriter, r *http.Request, c adminCtx) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	m := store.Merchant{Enabled: true}
	if id != 0 {
		if err := s.Svc.DB.Take(&m, id).Error; err != nil {
			http.NotFound(w, r)
			return
		}
	} else {
		var ch store.Channel
		if s.Svc.DB.Where("enabled = ?", true).Order("id").Take(&ch).Error == nil {
			m.ChannelID = ch.ID
		}
	}
	s.render(w, http.StatusOK, "merchant", s.merchantData(c, m))
}

func (s *Server) merchantSave(w http.ResponseWriter, r *http.Request, c adminCtx) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var m store.Merchant
	if id != 0 {
		if err := s.Svc.DB.Take(&m, id).Error; err != nil {
			http.NotFound(w, r)
			return
		}
	}
	f := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	cid, _ := strconv.ParseUint(f("channel_id"), 10, 64)
	m.Name, m.MerchantID, m.ChannelID, m.NotifyURL, m.Remark = f("name"), f("merchant_id"), uint(cid), f("notify_url"), f("remark")
	m.SignKey, m.EncKey = strings.ToLower(f("sign_key")), strings.ToLower(f("enc_key"))
	m.Enabled = r.FormValue("enabled") == "1"
	fail := func(msg string) {
		c.Err = msg
		s.render(w, http.StatusBadRequest, "merchant", s.merchantData(c, m))
	}
	if m.Name == "" {
		fail("请填写名称")
		return
	}
	if _, err := vpnproto.NewCodec(m.MerchantID, m.SignKey, m.EncKey); err != nil {
		fail(err.Error())
		return
	}
	if m.NotifyURL != "" {
		if u, err := url.Parse(m.NotifyURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			fail("通知地址必须是 http(s) 地址")
			return
		}
	}
	var ch store.Channel
	if err := s.Svc.DB.Take(&ch, m.ChannelID).Error; err != nil {
		fail("请选择支付通道（先在「支付通道」里添加）")
		return
	}
	var dup int64
	s.Svc.DB.Model(&store.Merchant{}).Where("merchant_id = ? AND id <> ?", m.MerchantID, m.ID).Count(&dup)
	if dup > 0 {
		fail("商户号已存在")
		return
	}
	if err := s.Svc.DB.Save(&m).Error; err != nil {
		fail("保存失败：" + err.Error())
		return
	}
	back(w, r, fmt.Sprintf("/admin/merchants/%d", m.ID), "ok", "商户已保存")
}

func (s *Server) merchantTestNotify(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	id, _ := pathID(r)
	var m store.Merchant
	if err := s.Svc.DB.Take(&m, id).Error; err != nil {
		http.NotFound(w, r)
		return
	}
	path := fmt.Sprintf("/admin/merchants/%d", m.ID)
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		target = m.NotifyURL
	}
	if target == "" {
		var o store.Order
		if s.Svc.DB.Where("merchant_id = ? AND notify_url <> '' AND is_test = ?", m.MerchantID, false).Order("id DESC").Take(&o).Error == nil {
			target = o.NotifyURL
		}
	}
	if u, err := url.Parse(target); target == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		back(w, r, path, "err", "请填写 VPN 的支付结果通知地址，例如 https://api.example.com/pay/gateway/notify")
		return
	}
	res := s.Svc.TestNotify(r.Context(), &m, target)
	switch {
	case strings.Contains(res.Detail, "ORDER_NOT_FOUND"):
		back(w, r, path, "ok", "VPN 返回 ORDER_NOT_FOUND：通知地址和密钥都正确（测试订单本来就不存在）")
	case strings.Contains(res.Detail, "BAD_SIGN"):
		back(w, r, path, "err", "VPN 返回 BAD_SIGN：签名密钥与 VPN 后台不一致。"+res.Detail)
	default:
		back(w, r, path, "err", "测试结果："+res.Detail)
	}
}

func (s *Server) merchantDelete(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	id, _ := pathID(r)
	s.Svc.DB.Delete(&store.Merchant{}, id)
	back(w, r, "/admin/merchants", "ok", "商户已删除（历史订单保留）")
}

func (s *Server) genKeys(w http.ResponseWriter, _ *http.Request, _ adminCtx) {
	mid, sk, ek, err := vpnproto.GenerateKeys()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"merchant_id": mid, "sign_key": sk, "enc_key": ek})
}

// ---- settings ----

func (s *Server) settings(w http.ResponseWriter, r *http.Request, c adminCtx) {
	pub := c.PublicURL
	guessed := false
	if pub == "" {
		scheme := "http"
		if s.secureCookie(r) {
			scheme = "https"
		}
		pub, guessed = scheme+"://"+r.Host, true
	}
	var counts struct{ Orders, Channels, Merchants, Tests int64 }
	db := s.Svc.DB
	db.Model(&store.Order{}).Count(&counts.Orders)
	db.Model(&store.Channel{}).Count(&counts.Channels)
	db.Model(&store.Merchant{}).Count(&counts.Merchants)
	db.Model(&store.TestRun{}).Count(&counts.Tests)
	up := time.Since(s.started).Round(time.Minute)
	s.render(w, http.StatusOK, "settings", map[string]any{"C": c, "PublicURL": pub, "Guessed": guessed,
		"User": store.GetSetting(db, settingUser), "Counts": counts, "Uptime": fmtDuration(up),
		"Started": s.started, "GoVersion": runtime.Version()})
}

func fmtDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, h)
	case h > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", h, m)
	}
	return fmt.Sprintf("%d 分钟", m)
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	pub := strings.TrimRight(strings.TrimSpace(r.FormValue("public_url")), "/")
	u, err := url.Parse(pub)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" {
		back(w, r, "/admin/settings", "err", "对外地址必须是 http(s)://域名[:端口][/路径]")
		return
	}
	if err := store.SetSetting(s.Svc.DB, service.SettingPublicURL, pub); err != nil {
		back(w, r, "/admin/settings", "err", err.Error())
		return
	}
	back(w, r, "/admin/settings", "ok", "对外地址已保存")
}

func (s *Server) passwordSave(w http.ResponseWriter, r *http.Request, c adminCtx) {
	db := s.Svc.DB
	if bcrypt.CompareHashAndPassword([]byte(store.GetSetting(db, settingPassHash)), []byte(r.FormValue("old"))) != nil {
		back(w, r, "/admin/settings", "err", "原密码错误")
		return
	}
	if r.FormValue("new") != r.FormValue("new2") {
		back(w, r, "/admin/settings", "err", "两次输入的新密码不一致")
		return
	}
	if err := SetAdminPassword(db, r.FormValue("new")); err != nil {
		back(w, r, "/admin/settings", "err", err.Error())
		return
	}
	s.setSession(w, r, s.newSession(c.User), int(sessionTTL/time.Second))
	back(w, r, "/admin/settings", "ok", "密码已修改，其他登录已失效")
}
