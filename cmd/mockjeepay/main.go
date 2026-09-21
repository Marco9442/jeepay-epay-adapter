package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marco9442/jeepay-epay-adapter/internal/jeepay"
)

// mock-jeepay 只实现适配器会用到的 Jeepay 商户 API：统一下单、查单、以及一页模拟微信扫码付款。
func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	s := &server{
		mchNo:     env("JEEPAY_MCH_NO", "M1680000001"),
		appID:     env("JEEPAY_APP_ID", "60cc09bce4b0f1c0b83761c9"),
		secret:    env("JEEPAY_APP_SECRET", "jeepay-dev-app-secret"),
		publicURL: strings.TrimRight(env("PUBLIC_BASE_URL", "http://127.0.0.1:19216"), "/"),
		log:       log,
		orders:    map[string]*order{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /api/pay/unifiedOrder", s.unifiedOrder)
	mux.HandleFunc("POST /api/pay/query", s.query)
	mux.HandleFunc("GET /wxpay/{id}", s.wxPage)
	mux.HandleFunc("POST /wxpay/{id}/pay", s.wxPay)
	addr := env("HTTP_ADDR", ":9216")
	log.Info("mock-jeepay 已启动", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Error("退出", "err", err)
		os.Exit(1)
	}
}

type order struct {
	PayOrderID string
	MchOrderNo string
	Amount     int64
	WayCode    string
	NotifyURL  string
	State      int
	Subject    string
	Body       string
	CreatedAt  int64
}

type server struct {
	mchNo, appID, secret, publicURL string
	log                             *slog.Logger
	mu                              sync.Mutex
	orders                          map[string]*order
}

func (s *server) unifiedOrder(w http.ResponseWriter, r *http.Request) {
	params, err := readJSON(r)
	if err != nil {
		writeFail(w, "参数错误")
		return
	}
	if err := s.auth(params); err != nil {
		writeFail(w, err.Error())
		return
	}
	mchOrderNo := params["mchOrderNo"]
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.orders {
		if o.MchOrderNo == mchOrderNo {
			writeFail(w, "商户订单["+mchOrderNo+"]已存在")
			return
		}
	}
	amount, _ := strconv.ParseInt(params["amount"], 10, 64)
	if amount < 1 {
		writeFail(w, "支付金额不能为空")
		return
	}
	id := "P" + time.Now().UTC().Format("20060102150405") + randHex(3)
	o := &order{
		PayOrderID: id,
		MchOrderNo: mchOrderNo,
		Amount:     amount,
		WayCode:    params["wayCode"],
		NotifyURL:  params["notifyUrl"],
		State:      jeepay.StateIng,
		Subject:    params["subject"],
		Body:       params["body"],
		CreatedAt:  time.Now().UnixMilli(),
	}
	s.orders[id] = o
	codeURL := s.publicURL + "/wxpay/" + id
	data := map[string]string{
		"payOrderId":  id,
		"mchOrderNo":  mchOrderNo,
		"orderState":  strconv.Itoa(jeepay.StateIng),
		"payDataType": jeepay.PayDataCodeURL,
		"payData":     codeURL,
	}
	writeOK(w, data, s.secret)
}

func (s *server) query(w http.ResponseWriter, r *http.Request) {
	params, err := readJSON(r)
	if err != nil {
		writeFail(w, "参数错误")
		return
	}
	if err := s.auth(params); err != nil {
		writeFail(w, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.find(params["payOrderId"], params["mchOrderNo"])
	if o == nil {
		writeFail(w, "订单不存在")
		return
	}
	writeOK(w, s.queryData(o), s.secret)
}

func (s *server) wxPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	o := s.orders[id]
	s.mu.Unlock()
	if o == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if o.State == jeepay.StateSuccess {
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>模拟微信支付</title>
<body style="font-family:sans-serif;text-align:center;margin-top:48px">
<h2>已支付</h2>
<p>订单 %s · ¥ %.2f</p>
</body>`, o.PayOrderID, float64(o.Amount)/100)
		return
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>模拟微信支付</title>
<body style="font-family:sans-serif;text-align:center;margin-top:48px">
<h2>模拟微信扫码</h2>
<p>订单 %s · ¥ %.2f</p>
<form method="post" action="/wxpay/%s/pay"><button style="font-size:18px;padding:8px 20px">确认付款</button></form>
</body>`, o.PayOrderID, float64(o.Amount)/100, o.PayOrderID)
}

func (s *server) wxPay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	o := s.orders[id]
	if o != nil && o.State != jeepay.StateSuccess {
		o.State = jeepay.StateSuccess
	}
	s.mu.Unlock()
	if o == nil {
		http.NotFound(w, r)
		return
	}
	go s.notify(o)
	http.Redirect(w, r, "/wxpay/"+id, http.StatusFound)
}

func (s *server) notify(o *order) {
	params := s.queryData(o)
	params["successTime"] = strconv.FormatInt(time.Now().UnixMilli(), 10)
	body, err := postMerchantNotify(o.NotifyURL, params, s.secret)
	s.log.Info("已通知商户", "url", o.NotifyURL, "resp", body, "err", err)
}

func postMerchantNotify(notifyURL string, params map[string]string, secret string) (string, error) {
	signed := make(map[string]string, len(params)+2)
	for k, v := range params {
		if v == "" {
			continue
		}
		signed[k] = v
	}
	signed["reqTime"] = strconv.FormatInt(time.Now().UnixMilli(), 10)
	signed["sign"] = jeepay.Sign(signed, secret)
	form := url.Values{}
	for k, v := range signed {
		form.Set(k, v)
	}
	resp, err := http.Post(notifyURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return strings.TrimSpace(string(b)), nil
}

func (s *server) queryData(o *order) map[string]string {
	return map[string]string{
		"payOrderId": o.PayOrderID,
		"mchNo":      s.mchNo,
		"appId":      s.appID,
		"mchOrderNo": o.MchOrderNo,
		"ifCode":     "wxpay",
		"wayCode":    o.WayCode,
		"amount":     strconv.FormatInt(o.Amount, 10),
		"currency":   "cny",
		"state":      strconv.Itoa(o.State),
		"subject":    o.Subject,
		"body":       o.Body,
		"createdAt":  strconv.FormatInt(o.CreatedAt, 10),
	}
}

func (s *server) find(payOrderID, mchOrderNo string) *order {
	if payOrderID != "" {
		return s.orders[payOrderID]
	}
	for _, o := range s.orders {
		if o.MchOrderNo == mchOrderNo {
			return o
		}
	}
	return nil
}

func (s *server) auth(params map[string]string) error {
	if params["mchNo"] != s.mchNo || params["appId"] != s.appID {
		return fmt.Errorf("商户或商户应用不存在")
	}
	if !jeepay.Verify(params, s.secret) {
		return fmt.Errorf("验签失败")
	}
	return nil
}

func readJSON(r *http.Request) (map[string]string, error) {
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64:
			if t == float64(int64(t)) {
				out[k] = strconv.FormatInt(int64(t), 10)
			} else {
				out[k] = strconv.FormatFloat(t, 'f', -1, 64)
			}
		default:
			out[k] = fmt.Sprint(t)
		}
	}
	return out, nil
}

func writeOK(w http.ResponseWriter, data map[string]string, secret string) {
	sign := jeepay.Sign(data, secret)
	writeJSON(w, map[string]any{"code": 0, "msg": "SUCCESS", "data": toAny(data), "sign": sign})
}

func writeFail(w http.ResponseWriter, msg string) {
	writeJSON(w, map[string]any{"code": 9999, "msg": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func toAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if k == "amount" || k == "orderState" || k == "state" || k == "createdAt" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				out[k] = n
				continue
			}
		}
		out[k] = v
	}
	return out
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
