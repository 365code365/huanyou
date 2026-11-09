package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"paycenter/internal/service"
	"paycenter/internal/store"
	"paycenter/internal/xinlian"
)

type runRow struct {
	R       store.TestRun
	O       *store.Order
	Notices int64
	Steps   int
	PassN   int
}

func errMsg(err error) string {
	var ue *service.UserError
	if errors.As(err, &ue) {
		return ue.Msg
	}
	return err.Error()
}

func (s *Server) testsPage(w http.ResponseWriter, r *http.Request, c adminCtx) {
	if no := r.URL.Query().Get("no"); no != "" { // return_url of a test checkout
		var run store.TestRun
		if s.Svc.DB.Where("out_trade_no = ?", no).Take(&run).Error == nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/tests/%d", run.ID), http.StatusFound)
			return
		}
	}
	var merchants []store.Merchant
	s.Svc.DB.Where("enabled = ?", true).Order("id").Find(&merchants)
	var runs []store.TestRun
	s.Svc.DB.Order("id DESC").Limit(50).Find(&runs)
	rows := make([]runRow, 0, len(runs))
	for i := range runs {
		row := runRow{R: runs[i], O: s.Svc.TestOrder(&runs[i])}
		s.Svc.DB.Model(&store.TestNotice{}).Where("run_id = ?", runs[i].ID).Count(&row.Notices)
		for _, st := range service.Steps(&runs[i]) {
			row.Steps++
			if st.OK {
				row.PassN++
			}
		}
		rows = append(rows, row)
	}
	sel := r.URL.Query().Get("merchant")
	if sel == "" && len(merchants) > 0 {
		sel = merchants[0].MerchantID
	}
	s.render(w, http.StatusOK, "tests", map[string]any{"C": c, "Merchants": merchants, "Rows": rows, "Sel": sel})
}

func (s *Server) testCreate(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	cents, err := xinlian.YuanToCents(r.FormValue("amount"))
	if err != nil {
		back(w, r, "/admin/tests", "err", "金额格式不对，例如 0.01")
		return
	}
	run, err := s.Svc.CreateTestRun(r.FormValue("merchant_id"), cents, r.FormValue("subject"), "manual")
	if err != nil {
		back(w, r, "/admin/tests", "err", errMsg(err))
		return
	}
	back(w, r, fmt.Sprintf("/admin/tests/%d", run.ID), "ok", "测试订单已生成：打开下单链接或扫码进入收银台")
}

func (s *Server) testSelf(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	cents, err := xinlian.YuanToCents(r.FormValue("amount"))
	if err != nil {
		back(w, r, "/admin/tests", "err", "金额格式不对，例如 0.01")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	run, err := s.Svc.RunSelfTest(ctx, r.FormValue("merchant_id"), cents, r.FormValue("upstream") == "1")
	if err != nil && run == nil {
		back(w, r, "/admin/tests", "err", errMsg(err))
		return
	}
	path := fmt.Sprintf("/admin/tests/%d", run.ID)
	if run.Passed {
		back(w, r, path, "ok", "自检通过：整条链路正常")
		return
	}
	back(w, r, path, "err", "自检未通过，请查看下面失败的步骤")
}

func (s *Server) loadRun(r *http.Request) (*store.TestRun, error) {
	id, ok := pathID(r)
	if !ok || id == 0 {
		return nil, errors.New("bad id")
	}
	var run store.TestRun
	if err := s.Svc.DB.Take(&run, id).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Server) testDetail(w http.ResponseWriter, r *http.Request, c adminCtx) {
	run, err := s.loadRun(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderTest(w, c, run, nil)
}

type kv struct{ K, V string }

func (s *Server) testStateOf(run *store.TestRun) (string, *store.Order, int64) {
	o := s.Svc.TestOrder(run)
	var n int64
	s.Svc.DB.Model(&store.TestNotice{}).Where("run_id = ?", run.ID).Count(&n)
	st := "none"
	if o != nil {
		st = o.Status + "/" + o.NotifyStatus + "/" + fmt.Sprint(o.NotifyAttempts)
	}
	return fmt.Sprintf("%s|%d", st, n), o, n
}

func (s *Server) renderTest(w http.ResponseWriter, c adminCtx, run *store.TestRun, trace *service.QueryTrace) {
	state, o, _ := s.testStateOf(run)
	var m store.Merchant
	s.Svc.DB.Where("merchant_id = ?", run.MerchantID).Take(&m)
	var notices []store.TestNotice
	s.Svc.DB.Where("run_id = ?", run.ID).Order("id DESC").Find(&notices)
	steps := service.Steps(run)
	passN, total := 0, int64(0)
	for _, st := range steps {
		if st.OK {
			passN++
		}
		total += st.Ms
	}
	var params []kv
	if u, err := url.Parse(run.PayLink); err == nil {
		q := u.Query()
		for k := range q {
			params = append(params, kv{k, q.Get(k)})
		}
		sort.Slice(params, func(i, j int) bool { return params[i].K < params[j].K })
	}
	var qr template.URL
	if png, err := qrcode.Encode(run.PayLink, qrcode.Medium, 240); err == nil {
		qr = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
	}
	s.render(w, http.StatusOK, "test", map[string]any{"C": c, "R": run, "M": m, "O": o, "Notices": notices,
		"Steps": steps, "PassN": passN, "TotalMs": total, "Params": params, "QR": qr, "Trace": trace, "State": state,
		"CanSimulate": o != nil && o.IsTest && o.Status != store.OrderPaid,
		"Paid":        o != nil && o.Status == store.OrderPaid, "Expired": o == nil && time.Since(run.CreatedAt) > 30*time.Minute})
}

func (s *Server) testState(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	run, err := s.loadRun(r)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"state": ""})
		return
	}
	st, _, _ := s.testStateOf(run)
	writeJSON(w, http.StatusOK, map[string]string{"state": st})
}

func (s *Server) testSimulate(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	run, err := s.loadRun(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := fmt.Sprintf("/admin/tests/%d", run.ID)
	o := s.Svc.TestOrder(run)
	if o == nil {
		back(w, r, path, "err", "请先打开下单链接（会在支付中心建单）")
		return
	}
	if err := s.Svc.SimulatePay(o); err != nil {
		back(w, r, path, "err", errMsg(err))
		return
	}
	time.Sleep(800 * time.Millisecond) // let the notification arrive before the page reloads
	back(w, r, path, "ok", "已模拟支付成功，并向模拟 VPN 发送通知")
}

func (s *Server) testQuery(w http.ResponseWriter, r *http.Request, c adminCtx) {
	run, err := s.loadRun(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	t := s.Svc.TestQuery(ctx, run.MerchantID, run.OutTradeNo)
	if strings.Contains(t.Err, "ORDER_NOT_FOUND") {
		c.Ok = "支付中心返回 404 ORDER_NOT_FOUND：还没有人打开下单链接（这是正常的 VPN 语义）"
	} else if t.Err != "" {
		c.Err = "查询失败：" + t.Err
	} else {
		c.Ok = fmt.Sprintf("查询成功：status=%s，耗时 %d ms", t.Order.Status, t.Ms)
	}
	s.renderTest(w, c, run, &t)
}

func (s *Server) testRenotify(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	run, err := s.loadRun(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := fmt.Sprintf("/admin/tests/%d", run.ID)
	o := s.Svc.TestOrder(run)
	if o == nil {
		back(w, r, path, "err", "订单不存在")
		return
	}
	if err := s.Svc.Renotify(o); err != nil {
		back(w, r, path, "err", err.Error())
		return
	}
	time.Sleep(800 * time.Millisecond)
	back(w, r, path, "ok", "已重新发送通知")
}
