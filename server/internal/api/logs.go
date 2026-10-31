package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"vpnplatform/server/internal/i18n"
	"vpnplatform/server/internal/logbuf"
)

// Live console log for the admin panel, plus AI analysis of it.
//
//	GET  /admin/api/logs?after=ID&level=WARN&q=text&limit=500&wait=25
//	     wait > 0 long-polls up to that many seconds for new lines (live tail).
//	POST /admin/api/logs/ask {question, level, q, limit}
func (s *Server) registerAdminLogs(a func(string, http.HandlerFunc)) {
	a("GET /admin/api/logs", s.adminLogs)
	a("POST /admin/api/logs/ask", s.adminLogsAsk)
}

func logFilter(level, q string, after int64, limit int) logbuf.Filter {
	return logbuf.Filter{After: after, MinLevel: strings.ToUpper(level), Query: q, Limit: limit}
}

func (s *Server) adminLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	wait, _ := strconv.Atoi(q.Get("wait"))
	f := logFilter(q.Get("level"), q.Get("q"), after, limit)
	items, last := logbuf.Default.List(f)
	if len(items) == 0 && wait > 0 && after > 0 {
		// Long poll: hold the request until a line arrives (live tail).
		if wait > 25 {
			wait = 25
		}
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Now().Add(time.Duration(wait+10) * time.Second))
		deadline := time.Now().Add(time.Duration(wait) * time.Second)
		for len(items) == 0 && time.Now().Before(deadline) && r.Context().Err() == nil {
			logbuf.Default.Wait(r.Context(), last, time.Until(deadline))
			items, last = logbuf.Default.List(f)
			if len(items) == 0 {
				f.After = last // lines arrived but none match the filter
			}
		}
	}
	if items == nil {
		items = []logbuf.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "last_id": last, "started_at": startedAt})
}

const logSystemPrompt = `你是一名资深运维工程师，负责一套 VPN 服务平台的控制面（Go 编写的 vpn-api）。
系统组成：App 通过 /api/v1/* 登录、拉取节点和连接配置、下单；节点代理（vpn-node）通过 /internal/v1/* 同步用户和上报流量；
支付通过 /pay/*（支付中心通知）和本系统主动查询支付中心完成；管理后台 /admin/api/*；后台任务包括节点监测预警、IP 记录、订单对账、请求防护（guard）。
日志是 Go slog 文本格式：时间 级别 消息 key=value。
请根据用户提供的日志回答问题：
1. 先给结论（有没有问题、严重程度），再列出依据（引用具体日志的时间和内容）；
2. 说明可能原因和排查/修复建议，给出可执行的步骤；
3. 日志里看不出来的不要编造，直接说明还需要哪些信息；
4. 用简体中文，Markdown 格式，简洁。`

func (s *Server) adminLogsAsk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Question string `json:"question"`
		Level    string `json:"level"`
		Q        string `json:"q"`
		Limit    int    `json:"limit"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Question = strings.TrimSpace(in.Question)
	if in.Question == "" {
		in.Question = "分析这些日志：系统是否正常？有哪些错误或异常，原因和处理建议是什么？"
	}
	if len([]rune(in.Question)) > 2000 {
		writeErr(w, http.StatusBadRequest, "too_long", "问题太长")
		return
	}
	cfg := s.aiConfig(r)
	if !cfg.Ready() {
		writeErr(w, http.StatusBadRequest, "ai_not_configured", "未配置 AI 接口：请先在「多语言」页填写 AI 接口地址、密钥和模型")
		return
	}
	if in.Limit <= 0 || in.Limit > 1500 {
		in.Limit = 400
	}
	items, _ := logbuf.Default.List(logFilter(in.Level, in.Q, 0, in.Limit))
	// Keep the prompt bounded (newest lines win).
	const maxBytes = 60 << 10
	lines := make([]string, 0, len(items))
	size := 0
	for i := len(items) - 1; i >= 0; i-- {
		e := items[i]
		l := fmt.Sprintf("%s %s %s %s", e.Time.Format("01-02 15:04:05"), e.Level, e.Msg, e.Attrs)
		if size+len(l) > maxBytes {
			break
		}
		size += len(l) + 1
		lines = append(lines, l)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	cat := s.cat()
	ctxInfo := fmt.Sprintf("当前时间 %s；服务已运行 %s；节点 %d 个；数据库 %s；Go %s。",
		time.Now().Format("2006-01-02 15:04:05"), time.Since(startedAt).Round(time.Second), len(cat.Nodes), s.store.Driver(), runtime.Version())
	user := ctxInfo + "\n\n问题：" + in.Question + "\n\n日志（共 " + strconv.Itoa(len(lines)) + " 行，按时间先后）：\n```\n" + strings.Join(lines, "\n") + "\n```"
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(100 * time.Second))
	answer, err := i18n.Chat(ctx, cfg, logSystemPrompt, user)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "ai_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer, "lines": len(lines)})
}

// withAccessLog records what is worth seeing in the console: server errors,
// slow requests, and every admin change (an audit trail: who changed what,
// from which IP). Routine traffic (node syncs, app polling) is not logged.
func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		d := time.Since(start)
		p := r.URL.Path
		streaming := strings.HasSuffix(p, "/logs") || strings.HasSuffix(p, "/clone") || strings.HasSuffix(p, "/backup") ||
			strings.Contains(p, "/database/") || strings.HasSuffix(p, "/provision") || strings.HasSuffix(p, "/ask")
		switch {
		case sw.status >= 500:
			slog.Error("request failed", "method", r.Method, "path", p, "status", sw.status, "ms", d.Milliseconds(), "ip", s.clientIP(r))
		case strings.HasPrefix(p, "/admin/api/") && r.Method != http.MethodGet && p != "/admin/api/login":
			slog.Info("admin action", "method", r.Method, "path", p, "status", sw.status, "ms", d.Milliseconds(), "ip", s.clientIP(r))
		case p == "/admin/api/login" && sw.status != 200:
			slog.Warn("admin login failed", "status", sw.status, "ip", s.clientIP(r))
		case d > 3*time.Second && !streaming:
			slog.Warn("slow request", "method", r.Method, "path", p, "status", sw.status, "ms", d.Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach Flush / deadlines (streaming endpoints).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
