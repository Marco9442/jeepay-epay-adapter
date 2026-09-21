package app

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed pay.html
var payHTML string

type httpServer struct {
	svc  *Service
	log  *slog.Logger
	payT *template.Template
}

func Handler(svc *Service, log *slog.Logger) http.Handler {
	s := &httpServer{
		svc:  svc,
		log:  log,
		payT: template.Must(template.New("pay").Parse(payHTML)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /submit.php", s.submit)
	mux.HandleFunc("POST /submit.php", s.submit)
	mux.HandleFunc("GET /jeepay/notify", s.jeepayNotify)
	mux.HandleFunc("POST /jeepay/notify", s.jeepayNotify)
	mux.HandleFunc("GET /pay/{tradeNo}", s.payPage)
	mux.HandleFunc("GET /pay/{tradeNo}/qr.png", s.payQR)
	mux.HandleFunc("GET /pay/{tradeNo}/status", s.payStatus)
	mux.HandleFunc("GET /pay/{tradeNo}/return", s.payReturn)
	return mux
}

func (s *httpServer) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *httpServer) submit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	payURL, err := s.svc.Submit(formMap(r.Form), clientIP(r))
	if err != nil {
		s.log.Warn("易支付下单失败", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, payURL, http.StatusFound)
}

func (s *httpServer) jeepayNotify(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if err := s.svc.HandleJeepayNotify(r.Form); err != nil {
		s.log.Warn("Jeepay 通知处理失败", "err", err)
		http.Error(w, "fail", http.StatusBadRequest)
		return
	}
	_, _ = w.Write([]byte("success"))
}

func (s *httpServer) cashier(w http.ResponseWriter, r *http.Request) (Cashier, bool) {
	c, err := s.svc.Cashier(r.PathValue("tradeNo"))
	if err != nil {
		http.NotFound(w, r)
		return Cashier{}, false
	}
	return c, true
}

func (s *httpServer) payPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cashier(w, r)
	if !ok {
		return
	}
	if c.Paid() {
		http.Redirect(w, r, "/pay/"+c.TradeNo+"/return", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.payT.Execute(w, map[string]any{
		"TradeNo":     c.TradeNo,
		"TradeNoJSON": template.JS(jsonMust(c.TradeNo)),
		"Money":       c.Money,
		"PayData":     c.PayData,
	})
}

func (s *httpServer) payQR(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cashier(w, r)
	if !ok {
		return
	}
	png, err := qrcode.Encode(c.QRContent, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "二维码生成失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (s *httpServer) payStatus(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cashier(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"state":      c.State,
		"return_url": c.ReturnURL,
	})
}

func (s *httpServer) payReturn(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cashier(w, r)
	if !ok {
		return
	}
	if c.ReturnURL == "" {
		_, _ = w.Write([]byte("支付完成"))
		return
	}
	http.Redirect(w, r, c.ReturnURL, http.StatusFound)
}

func formMap(v url.Values) map[string]string {
	out := make(map[string]string, len(v))
	for k, vs := range v {
		if len(vs) == 0 {
			continue
		}
		out[k] = vs[0]
	}
	return out
}

func jsonMust(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
