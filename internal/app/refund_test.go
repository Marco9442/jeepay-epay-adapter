package app_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/marco9442/jeepay-epay-adapter/internal/app"
	"github.com/marco9442/jeepay-epay-adapter/internal/jeepay"
	"github.com/marco9442/jeepay-epay-adapter/internal/store"
)

func TestFullRefundDeductsCreditedDollarsOnce(t *testing.T) {
	env := newRefundEnv(t)
	env.insertPaid("P1", "USR2NOabc", "TUC10", 6441)

	body, status := env.postRefund(t, env.refundForm("R1", "P1", 6441, 6441, 2))
	if status != http.StatusOK || body != "success" {
		t.Fatalf("status %d body %s", status, body)
	}
	if env.subtracts != 1 || env.lastUser != 2 || env.lastQuota != 5_000_000 {
		t.Fatalf("subtracts=%d user=%d quota=%d", env.subtracts, env.lastUser, env.lastQuota)
	}

	body, status = env.postRefund(t, env.refundForm("R1", "P1", 6441, 6441, 2))
	if status != http.StatusOK || body != "success" || env.subtracts != 1 {
		t.Fatalf("retry status %d body %s subtracts %d", status, body, env.subtracts)
	}
}

func TestPartialAndFailedRefundDoNotDeduct(t *testing.T) {
	env := newRefundEnv(t)
	env.insertPaid("P1", "USR2NOabc", "TUC10", 6441)

	body, status := env.postRefund(t, env.refundForm("Rpartial", "P1", 6441, 1000, 2))
	if status != http.StatusOK || body != "success" || env.subtracts != 0 {
		t.Fatalf("partial status %d body %s subtracts %d", status, body, env.subtracts)
	}
	body, status = env.postRefund(t, env.refundForm("Rfail", "P1", 6441, 6441, 3))
	if status != http.StatusOK || body != "success" || env.subtracts != 0 {
		t.Fatalf("failed status %d body %s subtracts %d", status, body, env.subtracts)
	}
}

func TestRefundRejectsBadSignAndMissingToken(t *testing.T) {
	env := newRefundEnv(t)
	env.insertPaid("P1", "USR2NOabc", "TUC10", 6441)
	form := env.refundForm("R1", "P1", 6441, 6441, 2)
	form.Set("sign", "BAD")
	if _, status := env.postRefund(t, form); status != http.StatusBadRequest || env.subtracts != 0 {
		t.Fatalf("bad sign status %d", status)
	}

	env.svc.Cfg.NewAPIAdminToken = ""
	if _, status := env.postRefund(t, env.refundForm("R2", "P1", 6441, 6441, 2)); status != http.StatusBadRequest || env.subtracts != 0 {
		t.Fatalf("missing token status %d", status)
	}
}

func TestRefundedListReturnsOnlyThatUsersFullRefund(t *testing.T) {
	env := newRefundEnv(t)
	env.insertPaid("P2", "USR2NOaaa", "TUC10", 6441)
	env.insertPaid("P3", "USR3NObbb", "TUC1", 678)
	env.insertPaid("Ppartial", "USR2NOpart", "TUC10", 6441)
	if _, status := env.postRefund(t, env.refundForm("R2", "P2", 6441, 6441, 2)); status != http.StatusOK {
		t.Fatalf("user2 refund status %d", status)
	}
	if _, status := env.postRefund(t, env.refundForm("R3", "P3", 678, 678, 2)); status != http.StatusOK {
		t.Fatalf("user3 refund status %d", status)
	}
	if _, status := env.postRefund(t, env.refundForm("Rpart", "Ppartial", 6441, 1000, 2)); status != http.StatusOK {
		t.Fatalf("partial status %d", status)
	}

	body, status := env.getRefunded(t, "session=user2", "")
	if status != http.StatusOK || body != `{"trade_nos":["USR2NOaaa"]}`+"\n" {
		t.Fatalf("user2 status %d body %s", status, body)
	}
	body, status = env.getRefunded(t, "session=user3", "")
	if status != http.StatusOK || body != `{"trade_nos":["USR3NObbb"]}`+"\n" {
		t.Fatalf("user3 status %d body %s", status, body)
	}
	body, status = env.getRefunded(t, "", "Bearer user-2")
	if status != http.StatusOK || !strings.Contains(body, "USR2NOaaa") || strings.Contains(body, "USR3NObbb") {
		t.Fatalf("bearer status %d body %s", status, body)
	}
	if _, status := env.getRefunded(t, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous status %d", status)
	}
}

func TestPendingRefundDoesNotSubtractTwice(t *testing.T) {
	env := newRefundEnv(t)
	env.insertPaid("P1", "USR2NOabc", "TUC10", 6441)
	env.logHit = true
	if _, err := env.st.ClaimRefund("R1", "P1", 2, 5_000_000); err != nil {
		t.Fatal(err)
	}
	body, status := env.postRefund(t, env.refundForm("R1", "P1", 6441, 6441, 2))
	if status != http.StatusOK || body != "success" || env.subtracts != 0 {
		t.Fatalf("status %d body %s subtracts %d", status, body, env.subtracts)
	}
}

type refundEnv struct {
	st        *store.Store
	svc       *app.Service
	adapter   *httptest.Server
	secret    string
	mchNo     string
	appID     string
	mu        sync.Mutex
	subtracts int
	lastUser  int
	lastQuota int
	logHit    bool
}

func newRefundEnv(t *testing.T) *refundEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	env := &refundEnv{st: st, secret: "jeepay-secret", mchNo: "M1", appID: "A1"}
	newapiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"USD"}}`))
		case r.URL.Path == "/api/user/self":
			switch {
			case r.Header.Get("Cookie") == "session=user2" || r.Header.Get("Authorization") == "Bearer user-2":
				_, _ = w.Write([]byte(`{"success":true,"data":{"id":2}}`))
			case r.Header.Get("Cookie") == "session=user3":
				_, _ = w.Write([]byte(`{"success":true,"data":{"id":3}}`))
			default:
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false}`))
			}
		case r.URL.Path == "/api/user/manage":
			if r.Header.Get("Authorization") != "Bearer admin-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false,"message":"unauthorized"}`))
				return
			}
			var req struct {
				ID    int `json:"id"`
				Value int `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			env.mu.Lock()
			env.subtracts++
			env.lastUser = req.ID
			env.lastQuota = req.Value
			env.mu.Unlock()
			_, _ = w.Write([]byte(`{"success":true,"message":""}`))
		case r.URL.Path == "/api/log/":
			env.mu.Lock()
			hit := env.logHit
			env.mu.Unlock()
			if !hit {
				_, _ = w.Write([]byte(`{"success":true,"data":{"items":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"items":[{"user_id":2,"created_at":4102444800,"content":"Decreased user quota by 5000000"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(newapiSrv.Close)
	env.svc = app.New(app.Config{
		PublicBaseURL:    "http://127.0.0.1",
		InternalBaseURL:  "http://127.0.0.1",
		EpayPID:          "1000",
		EpayKey:          "epay-key",
		JeepayBaseURL:    "http://127.0.0.1",
		JeepayMchNo:      env.mchNo,
		JeepayAppID:      env.appID,
		JeepayAppSecret:  env.secret,
		WayCodeWxpay:     "WX_NATIVE",
		NewAPIBaseURL:    newapiSrv.URL,
		NewAPIAdminToken: "admin-token",
	}, st, newapiSrv.Client())
	env.adapter = httptest.NewServer(app.Handler(env.svc, testLog()))
	t.Cleanup(env.adapter.Close)
	return env
}

func (e *refundEnv) insertPaid(payOrderID, outTradeNo, name string, fen int64) {
	order := &store.Order{
		TradeNo:          "T" + payOrderID,
		OutTradeNo:       outTradeNo,
		PID:              "1000",
		PayType:          "wxpay",
		Name:             name,
		Money:            "64.41",
		AmountFen:        fen,
		NotifyURL:        "http://newapi/notify",
		ReturnURL:        "http://newapi/return",
		JeepayPayOrderID: payOrderID,
		PayStatus:        store.PayPaid,
		NotifyStatus:     store.NotifySucceeded,
	}
	if err := e.st.Insert(order); err != nil {
		panic(err)
	}
	if err := e.st.MarkPaid(order.TradeNo, payOrderID); err != nil {
		panic(err)
	}
}

func (e *refundEnv) refundForm(refundID, payID string, payAmount, refundAmount, state int64) url.Values {
	params := map[string]string{
		"refundOrderId": refundID,
		"payOrderId":    payID,
		"mchNo":         e.mchNo,
		"appId":         e.appID,
		"mchRefundNo":   "M" + refundID,
		"payAmount":     strconv.FormatInt(payAmount, 10),
		"refundAmount":  strconv.FormatInt(refundAmount, 10),
		"currency":      "cny",
		"state":         strconv.FormatInt(state, 10),
		"reqTime":       "2",
	}
	params["sign"] = jeepay.Sign(params, e.secret)
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	return form
}

func (e *refundEnv) getRefunded(t *testing.T, cookie, authorization string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.adapter.URL+"/refunded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(raw), resp.StatusCode
}

func (e *refundEnv) postRefund(t *testing.T, form url.Values) (string, int) {
	t.Helper()
	resp, err := http.Post(e.adapter.URL+"/jeepay/refund-notify", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return strings.TrimSpace(string(raw)), resp.StatusCode
}

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
