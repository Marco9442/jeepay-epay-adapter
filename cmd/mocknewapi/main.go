package main

import (
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marco9442/jeepay-epay-adapter/internal/epay"
)

// mock-newapi 只覆盖 NewAPI 充值会走到的易支付路径：Purchase 表单跳转 + notify 入账。
func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	publicURL := strings.TrimRight(env("PUBLIC_BASE_URL", "http://127.0.0.1:13001"), "/")
	notifyURL := strings.TrimRight(env("NOTIFY_BASE_URL", publicURL), "/")
	s := &server{
		pid:       env("EPAY_PID", "1000"),
		key:       env("EPAY_KEY", "dev-epay-key"),
		payAddr:   strings.TrimRight(env("EPAY_PAY_ADDRESS", "http://127.0.0.1:18080"), "/"),
		selfURL:   publicURL,
		notifyURL: notifyURL,
		log:       log,
		orders:    map[string]*topup{},
		balance:   0,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", s.home)
	mux.HandleFunc("POST /api/user/pay", s.pay)
	mux.HandleFunc("GET /api/user/epay/notify", s.notify)
	mux.HandleFunc("POST /api/user/epay/notify", s.notify)
	mux.HandleFunc("GET /usage-logs", s.done)
	addr := env("HTTP_ADDR", ":3001")
	log.Info("mock-newapi 已启动", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Error("退出", "err", err)
		os.Exit(1)
	}
}

type topup struct {
	TradeNo string
	Money   string
	Type    string
	Paid    bool
}

type server struct {
	pid, key, payAddr, selfURL, notifyURL string
	log                                   *slog.Logger
	mu                                    sync.Mutex
	orders                                map[string]*topup
	balance                               float64
}

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.mu.Lock()
	bal := s.balance
	s.mu.Unlock()
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>NewAPI 充值（模拟）</title>
<body style="font-family:sans-serif;max-width:480px;margin:40px auto">
<h2>模拟 NewAPI 充值</h2>
<p>当前余额：<b>%.2f</b></p>
<form method="post" action="/api/user/pay">
  <p>金额（元）<input name="amount" value="10.00"></p>
  <p><button>微信支付</button></p>
  <input type="hidden" name="payment_method" value="wxpay">
</form>
<p style="color:#666;font-size:14px">此页只用于开发联调，生产请用真实 NewAPI 前端。</p>
</body>`, bal)
}

func (s *server) pay(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	money := strings.TrimSpace(r.FormValue("amount"))
	if money == "" {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	if _, err := strconv.ParseFloat(money, 64); err != nil {
		http.Error(w, "金额无效", http.StatusBadRequest)
		return
	}
	tradeNo := fmt.Sprintf("USR1NO%s%d", rand4(), time.Now().Unix())
	s.mu.Lock()
	s.orders[tradeNo] = &topup{TradeNo: tradeNo, Money: money, Type: "wxpay"}
	s.mu.Unlock()

	params := map[string]string{
		"pid":          s.pid,
		"type":         "wxpay",
		"out_trade_no": tradeNo,
		"notify_url":   s.notifyURL + "/api/user/epay/notify",
		"name":         "TUC" + money,
		"money":        money,
		"device":       "pc",
		"return_url":   s.selfURL + "/usage-logs",
	}
	params["sign"] = epay.Sign(params, s.key)
	params["sign_type"] = epay.SignTypeMD5

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<form id='f' method='post' action='%s/submit.php'>", html.EscapeString(s.payAddr))
	for k, v := range params {
		fmt.Fprintf(w, `<input type="hidden" name="%s" value="%s">`, html.EscapeString(k), html.EscapeString(v))
	}
	fmt.Fprint(w, `</form><script>document.getElementById('f').submit()</script>`)
}

func (s *server) notify(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	params := map[string]string{}
	src := r.PostForm
	if len(src) == 0 {
		src = r.Form
	}
	for k, vs := range src {
		if len(vs) > 0 {
			params[k] = vs[0]
		}
	}
	if !epay.Verify(params, s.key) {
		s.log.Warn("验签失败", "params", params)
		_, _ = w.Write([]byte("fail"))
		return
	}
	if params["trade_status"] != epay.StatusTradeSuccess {
		_, _ = w.Write([]byte("success"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.orders[params["out_trade_no"]]
	if o == nil {
		_, _ = w.Write([]byte("fail"))
		return
	}
	if o.Type != params["type"] {
		_, _ = w.Write([]byte("fail"))
		return
	}
	if !o.Paid {
		f, _ := strconv.ParseFloat(o.Money, 64)
		s.balance += f
		o.Paid = true
		s.log.Info("入账成功", "trade", o.TradeNo, "money", o.Money, "balance", s.balance)
	}
	_, _ = w.Write([]byte("success"))
}

func (s *server) done(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	bal := s.balance
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>充值结果</title>
<body style="font-family:sans-serif;max-width:480px;margin:40px auto">
<h2>已返回 NewAPI</h2>
<p>当前余额：<b>%.2f</b></p>
<p><a href="/">继续充值</a></p>
</body>`, bal)
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func rand4() string {
	return strconv.FormatInt(time.Now().UnixNano()%1e6, 10)
}
