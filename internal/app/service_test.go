package app_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marco9442/jeepay-epay-adapter/internal/app"
	"github.com/marco9442/jeepay-epay-adapter/internal/epay"
	"github.com/marco9442/jeepay-epay-adapter/internal/jeepay"
	"github.com/marco9442/jeepay-epay-adapter/internal/store"
)

func TestSubmitPayNotifyCredit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const (
		secret  = "jeepay-secret"
		mchNo   = "M1"
		appID   = "A1"
		epayPID = "1000"
		epayKey = "epay-key"
	)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	var jeepayURL string
	gotMchOrder := ""
	jeepaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&raw)
		params := map[string]string{}
		for k, v := range raw {
			params[k] = anyString(v)
		}
		if !jeepay.Verify(params, secret) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "msg": "验签失败"})
			return
		}
		if r.URL.Path != "/api/pay/unifiedOrder" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "msg": "unknown"})
			return
		}
		gotMchOrder = params["mchOrderNo"]
		data := map[string]string{
			"payOrderId":  "PTEST1",
			"mchOrderNo":  params["mchOrderNo"],
			"orderState":  "1",
			"payDataType": "codeUrl",
			"payData":     jeepayURL + "/wxpay/PTEST1",
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "SUCCESS", "data": toAny(data), "sign": jeepay.Sign(data, secret),
		})
	}))
	t.Cleanup(jeepaySrv.Close)
	jeepayURL = jeepaySrv.URL

	credited := make(chan string, 1)
	newapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		params := map[string]string{}
		for k, vs := range r.PostForm {
			if len(vs) > 0 {
				params[k] = vs[0]
			}
		}
		if !epay.Verify(params, epayKey) || params["trade_status"] != epay.StatusTradeSuccess {
			_, _ = w.Write([]byte("fail"))
			return
		}
		credited <- params["out_trade_no"]
		_, _ = w.Write([]byte("success"))
	}))
	t.Cleanup(newapi.Close)

	svc := app.New(app.Config{
		PublicBaseURL:   "http://127.0.0.1",
		InternalBaseURL: "http://127.0.0.1",
		EpayPID:         epayPID,
		EpayKey:         epayKey,
		JeepayBaseURL:   jeepaySrv.URL,
		JeepayMchNo:     mchNo,
		JeepayAppID:     appID,
		JeepayAppSecret: secret,
		WayCodeWxpay:    "WX_NATIVE",
	}, st, jeepaySrv.Client())
	adapter := httptest.NewServer(app.Handler(svc, log))
	t.Cleanup(adapter.Close)
	svc.Cfg.PublicBaseURL = adapter.URL
	svc.Cfg.InternalBaseURL = adapter.URL

	wkr := &app.Worker{Svc: svc, Log: log, Every: time.Hour, MaxTry: 5, HTTP: newapi.Client()}

	params := map[string]string{
		"pid":          epayPID,
		"type":         "wxpay",
		"out_trade_no": "USR1NOTEST1",
		"notify_url":   newapi.URL + "/api/user/epay/notify",
		"name":         "TUC10",
		"money":        "10.00",
		"device":       "pc",
		"return_url":   newapi.URL + "/usage-logs",
	}
	params["sign"] = epay.Sign(params, epayKey)
	params["sign_type"] = "MD5"
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequest(http.MethodPost, adapter.URL+"/submit.php", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("submit status %d", resp.StatusCode)
	}
	if gotMchOrder != "USR1NOTEST1" {
		t.Fatalf("jeepay mchOrderNo=%s", gotMchOrder)
	}

	nparams := map[string]string{
		"payOrderId": "PTEST1",
		"mchNo":      mchNo,
		"appId":      appID,
		"mchOrderNo": "USR1NOTEST1",
		"ifCode":     "wxpay",
		"wayCode":    "WX_NATIVE",
		"amount":     "1000",
		"currency":   "cny",
		"state":      "2",
		"subject":    "TUC10",
		"body":       "TUC10",
		"createdAt":  "1",
		"reqTime":    "2",
	}
	nparams["sign"] = jeepay.Sign(nparams, secret)
	nform := url.Values{}
	for k, v := range nparams {
		nform.Set(k, v)
	}
	nresp, err := http.Post(adapter.URL+"/jeepay/notify", "application/x-www-form-urlencoded", strings.NewReader(nform.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(nresp.Body)
	_ = nresp.Body.Close()
	if !strings.EqualFold(strings.TrimSpace(string(body)), "success") {
		t.Fatalf("notify resp %s", body)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		wkr.Tick()
		select {
		case got := <-credited:
			if got != "USR1NOTEST1" {
				t.Fatalf("credited %s", got)
			}
			return
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatal("NewAPI 未入账")
}

func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return fmt.Sprint(t)
	}
}

func toAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
