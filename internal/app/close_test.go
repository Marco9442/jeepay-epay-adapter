package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/marco9442/jeepay-epay-adapter/internal/epay"
	"github.com/marco9442/jeepay-epay-adapter/internal/jeepay"
	"github.com/marco9442/jeepay-epay-adapter/internal/store"
)

func TestCloseStepWaitsAfterChannelClose(t *testing.T) {
	o := &store.Order{PayStatus: store.PayCreated, ExpireAt: 1000, ChannelClosedAt: 900}
	if closeStepOf(o, 900+channelGraceSec-1) != stepWait {
		t.Fatal("渠道刚关，应等待")
	}
	if closeStepOf(o, 900+channelGraceSec) != stepRequery {
		t.Fatal("等待结束后应再查")
	}
}

func TestDecideQueryDoesNotExpireOnSuccessOrMismatch(t *testing.T) {
	paid := decideQuery(jeepay.StateSuccess, 1000, 1000, false, 0, 1000)
	if !paid.markPaid || paid.callClose || paid.expire {
		t.Fatalf("成功且金额一致应只记已支付: %+v", paid)
	}
	bad := decideQuery(jeepay.StateSuccess, 1, 1000, false, 0, 1000)
	if !bad.block || bad.markPaid || bad.expire {
		t.Fatalf("金额不一致应停止关单: %+v", bad)
	}
}

func TestDecideQueryStateSixWaitsForGrace(t *testing.T) {
	early := decideQuery(jeepay.StateClosed, 0, 1000, false, 1000, 1000)
	if early.expire || early.callClose {
		t.Fatalf("未到宽限不应关钱包: %+v", early)
	}
	late := decideQuery(jeepay.StateClosed, 0, 1000, false, 1000+missedGraceSec, 1000)
	if !late.expire || late.callClose {
		t.Fatalf("过了宽限应关钱包且不再调渠道: %+v", late)
	}
}

func TestDecideCloseIngSuccessDoesNotExpire(t *testing.T) {
	record, block, expire := decideClose(jeepay.CloseSuccess, true, 5000, 1000)
	if !record || block || expire {
		t.Fatal("支付中关单成功只记渠道已关")
	}
}

func TestDecideCloseInitSuccessDoesNotRecordChannel(t *testing.T) {
	record, block, expire := decideClose(jeepay.CloseSuccess, false, 1000, 1000)
	if record || block || expire {
		t.Fatal("未支付中的关单成功不能当成渠道已关")
	}
	_, _, expire = decideClose(jeepay.CloseSuccess, false, 1000+missedGraceSec, 1000)
	if !expire {
		t.Fatal("宽限后才关钱包")
	}
}

func TestDecideClosePaidBlocksAndFailureDoesNotExpireIng(t *testing.T) {
	_, block, expire := decideClose(jeepay.ClosePaid, true, 1000+missedGraceSec, 1000)
	if !block || expire {
		t.Fatal("已支付不能关钱包")
	}
	_, block, expire = decideClose(jeepay.CloseFailed, true, 1000+missedGraceSec, 1000)
	if block || expire {
		t.Fatal("支付中关单失败不能关钱包")
	}
	_, _, expire = decideClose(jeepay.CloseRejected, true, 1000+missedGraceSec, 1000)
	if !expire {
		t.Fatal("渠道拒绝后，过了宽限应关钱包")
	}
}

func TestCloserIngCloseWaitsThenExpires(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT1", 10_000)
	fx.state = jeepay.StateIng
	fx.closeKind = "success"
	fx.wallet.result = walletExpired

	fx.tick(10_000 - closeLeadSec)
	if fx.closes != 1 || fx.wallet.got != "" {
		t.Fatalf("应关渠道且先不关钱包 closes=%d wallet=%s", fx.closes, fx.wallet.got)
	}
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || got.ChannelClosedAt == 0 || got.PayStatus != store.PayCreated {
		t.Fatalf("应记下渠道已关且仍是处理中: %+v %v", got, err)
	}

	fx.state = jeepay.StateClosed
	fx.tick(got.ChannelClosedAt + channelGraceSec - 1)
	if fx.wallet.got != "" {
		t.Fatal("两分钟内不能关钱包")
	}
	fx.tick(got.ChannelClosedAt + channelGraceSec)
	if fx.wallet.got != o.OutTradeNo {
		t.Fatalf("再查后应按商户单号关钱包，got=%s", fx.wallet.got)
	}
	got, err = fx.st.ByTradeNo(o.TradeNo)
	if err != nil || got.PayStatus != store.PayFailed {
		t.Fatalf("钱包确认后才记失败: %+v %v", got, err)
	}
}

func TestCloserQuerySuccessDoesNotClose(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT2", 10_000)
	fx.state = jeepay.StateSuccess
	fx.amount = 1000
	fx.tick(10_000)
	if fx.closes != 0 || fx.wallet.got != "" {
		t.Fatal("已支付不能关单")
	}
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || got.PayStatus != store.PayPaid {
		t.Fatalf("应记已支付: %+v %v", got, err)
	}
}

func TestCloserOrderPaidBlocks(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT3", 10_000)
	fx.state = jeepay.StateIng
	fx.closeKind = "paid"
	fx.tick(10_000 + missedGraceSec)
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || !got.ExpireBlocked || got.PayStatus != store.PayCreated || fx.wallet.got != "" {
		t.Fatalf("已支付应停止关单: %+v wallet=%s", got, fx.wallet.got)
	}
}

func TestCloserGenericCloseFailDoesNotExpire(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT4", 10_000)
	fx.state = jeepay.StateIng
	fx.closeKind = "failed"
	fx.tick(10_000 + missedGraceSec)
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || got.PayStatus != store.PayCreated || got.ExpireBlocked || fx.wallet.got != "" {
		t.Fatalf("关单失败不能关钱包: %+v", got)
	}
}

func TestCloserStateSixAfterGraceExpires(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT5", 10_000)
	fx.state = jeepay.StateClosed
	fx.wallet.result = walletExpired
	fx.tick(10_000 + missedGraceSec - 1)
	if fx.closes != 0 || fx.wallet.got != "" {
		t.Fatal("未到宽限不能关，也不能调渠道关单")
	}
	fx.tick(10_000 + missedGraceSec)
	if fx.closes != 0 || fx.wallet.got != o.OutTradeNo {
		t.Fatalf("过宽限只关钱包 closes=%d wallet=%s", fx.closes, fx.wallet.got)
	}
}

func TestCloserInitCloseDoesNotRecordChannel(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT6", 10_000)
	fx.state = jeepay.StateInit
	fx.closeKind = "success"
	fx.tick(10_000)
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || got.ChannelClosedAt != 0 || got.PayStatus != store.PayCreated || fx.closes != 1 {
		t.Fatalf("初始单关单成功不能记渠道已关: %+v closes=%d", got, fx.closes)
	}
}

func TestCloserWalletMissingDoesNotFail(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT7", 10_000)
	fx.state = jeepay.StateClosed
	fx.wallet.result = "missing"
	fx.tick(10_000 + missedGraceSec)
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || got.PayStatus != store.PayCreated {
		t.Fatalf("钱包没有这笔单不能记失败: %+v", got)
	}
}

func TestCloserWalletSuccessMarksPaid(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT8", 10_000)
	fx.state = jeepay.StateClosed
	fx.wallet.result = walletSuccess
	fx.tick(10_000 + missedGraceSec)
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || got.PayStatus != store.PayPaid {
		t.Fatalf("钱包已成功应记已支付: %+v", got)
	}
}

func TestMarkFailedIfCreatedDoesNotOverwritePaid(t *testing.T) {
	st := openStore(t)
	o := &store.Order{
		TradeNo: "TPAID", OutTradeNo: "OPAID", PID: "1000", PayType: "wxpay",
		Name: "充值", Money: "10.00", AmountFen: 1000,
		NotifyURL: "http://example/n", ReturnURL: "http://example/r",
		PayStatus: store.PayCreated, NotifyStatus: store.NotifyNone,
	}
	if err := st.Insert(o); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPaid(o.TradeNo, "P1"); err != nil {
		t.Fatal(err)
	}
	changed, err := st.MarkFailedIfCreated(o.TradeNo)
	if err != nil || changed {
		t.Fatalf("不能覆盖已支付 changed=%v err=%v", changed, err)
	}
	got, err := st.ByTradeNo(o.TradeNo)
	if err != nil || got.PayStatus != store.PayPaid {
		t.Fatalf("状态=%s err=%v", got.PayStatus, err)
	}
}

func TestSubmitRefusesWhenExpireNotReady(t *testing.T) {
	st := openStore(t)
	svc := appService(st)
	svc.Cfg.ExpireRequired = true
	svc.SetWallet(downWallet{})
	_, err := svc.Submit(signedEpay("NEW1"), "127.0.0.1")
	if err == nil || err.Error() != "关单未就绪" {
		t.Fatalf("err=%v", err)
	}
}

func TestSubmitKeepsExistingPayPageWhenExpireNotReady(t *testing.T) {
	st := openStore(t)
	o := &store.Order{
		TradeNo: "TEXIST", OutTradeNo: "EXIST1", PID: "1000", PayType: "wxpay",
		Name: "充值", Money: "10.00", AmountFen: 1000,
		NotifyURL: "http://example/n", ReturnURL: "http://example/r",
		PayData: "weixin://pay", PayStatus: store.PayCreated, NotifyStatus: store.NotifyNone,
	}
	if err := st.Insert(o); err != nil {
		t.Fatal(err)
	}
	svc := appService(st)
	svc.Cfg.ExpireRequired = true
	svc.SetWallet(downWallet{})
	url, err := svc.Submit(signedEpay("EXIST1"), "127.0.0.1")
	if err != nil || url == "" {
		t.Fatalf("已有付款码应继续返回收银台 url=%s err=%v", url, err)
	}
}

func TestHealthStaysUpWhenExpireNotReady(t *testing.T) {
	st := openStore(t)
	svc := appService(st)
	svc.Cfg.ExpireRequired = true
	svc.SetWallet(downWallet{})
	h := Handler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCloserAmountMismatchBlocks(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT9", 10_000)
	fx.state = jeepay.StateSuccess
	fx.amount = 1
	fx.tick(10_000)
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || !got.ExpireBlocked || got.PayStatus != store.PayCreated || fx.closes != 0 {
		t.Fatalf("金额不一致应停止关单且不记已支付: %+v closes=%d", got, fx.closes)
	}
}

func TestCloserNotEpayBlocks(t *testing.T) {
	fx := newCloseFixture(t)
	o := fx.insert("OUT10", 10_000)
	fx.state = jeepay.StateClosed
	fx.wallet.result = walletNotEpay
	fx.tick(10_000 + missedGraceSec)
	got, err := fx.st.ByTradeNo(o.TradeNo)
	if err != nil || !got.ExpireBlocked || got.PayStatus != store.PayCreated {
		t.Fatalf("非易支付应停止关单: %+v", got)
	}
}

func TestSubmitPassesExpiredTime(t *testing.T) {
	st := openStore(t)
	const secret = "jeepay-secret"
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		data := map[string]string{
			"payOrderId": "P1", "mchOrderNo": "TTL1", "orderState": "1",
			"payDataType": "codeUrl", "payData": "weixin://pay",
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "SUCCESS", "data": data, "sign": jeepay.Sign(data, secret),
		})
	}))
	t.Cleanup(srv.Close)
	svc := New(Config{
		PublicBaseURL: "http://127.0.0.1", InternalBaseURL: "http://127.0.0.1",
		EpayPID: "1000", EpayKey: "epay-key",
		JeepayBaseURL: srv.URL, JeepayMchNo: "M1", JeepayAppID: "A1", JeepayAppSecret: secret,
		WayCodeWxpay: "WX_NATIVE", OrderTTLSec: 7200, ExpireRequired: true,
	}, st, srv.Client())
	svc.SetWallet(&fakeWallet{})
	before := time.Now().Unix()
	if _, err := svc.Submit(signedEpay("TTL1"), "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"expiredTime":7200`)) {
		t.Fatalf("body=%s", raw)
	}
	got, err := st.ByOutTradeNo("1000", "TTL1")
	if err != nil || got.ExpireAt < before+7200 || got.ExpireAt > time.Now().Unix()+7200 {
		t.Fatalf("expire_at=%d err=%v", got.ExpireAt, err)
	}
}

func TestFromEnvTTL(t *testing.T) {
	t.Setenv("EPAY_PID", "1000")
	t.Setenv("EPAY_KEY", "k")
	t.Setenv("JEEPAY_BASE_URL", "http://pay")
	t.Setenv("JEEPAY_MCH_NO", "M")
	t.Setenv("JEEPAY_APP_ID", "A")
	t.Setenv("JEEPAY_APP_SECRET", "S")
	t.Setenv("PAY_ORDER_TTL_SEC", "")
	t.Setenv("PAY_EXPIRE_REQUIRED", "")
	cfg, err := FromEnv()
	if err != nil || cfg.OrderTTLSec != 7200 || !cfg.ExpireRequired {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	t.Setenv("PAY_ORDER_TTL_SEC", "3600")
	cfg, err = FromEnv()
	if err != nil || cfg.OrderTTLSec != 3600 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	t.Setenv("PAY_ORDER_TTL_SEC", "1800")
	if _, err := FromEnv(); err == nil {
		t.Fatal("非法截止时间应拒绝启动")
	}
	t.Setenv("PAY_ORDER_TTL_SEC", "7200")
	t.Setenv("PAY_EXPIRE_REQUIRED", "0")
	cfg, err = FromEnv()
	if err != nil || cfg.ExpireRequired {
		t.Fatalf("开发堆栈应能显式关闭关单 cfg=%+v err=%v", cfg, err)
	}
}

type downWallet struct{}

func (downWallet) Ready(context.Context) error { return context.DeadlineExceeded }
func (downWallet) Expire(context.Context, string) (string, error) {
	return "", context.DeadlineExceeded
}

type fakeWallet struct {
	result string
	got    string
}

func (f *fakeWallet) Ready(context.Context) error { return nil }
func (f *fakeWallet) Expire(_ context.Context, tradeNo string) (string, error) {
	f.got = tradeNo
	return f.result, nil
}

type closeFixture struct {
	t         *testing.T
	st        *store.Store
	state     int
	amount    int64
	closeKind string
	queries   int
	closes    int
	wallet    *fakeWallet
	closer    *Closer
}

func newCloseFixture(t *testing.T) *closeFixture {
	t.Helper()
	const secret = "jeepay-secret"
	fx := &closeFixture{t: t, st: openStore(t), state: jeepay.StateIng, amount: 1000, closeKind: "success", wallet: &fakeWallet{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pay/query":
			fx.queries++
			writeSigned(w, map[string]string{
				"payOrderId": "P1",
				"state":      strconv.Itoa(fx.state),
				"amount":     strconv.FormatInt(fx.amount, 10),
			}, secret)
		case "/api/pay/close":
			fx.closes++
			switch fx.closeKind {
			case "success":
				writeSigned(w, map[string]string{}, secret)
			case "paid":
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 9999, "msg": "ORDERPAID"})
			case "rejected":
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 9999, "msg": "当前订单不可关闭"})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 9999, "msg": "系统异常"})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	svc := New(Config{
		PublicBaseURL:   "http://127.0.0.1",
		JeepayBaseURL:   srv.URL,
		JeepayMchNo:     "M1",
		JeepayAppID:     "A1",
		JeepayAppSecret: secret,
	}, fx.st, srv.Client())
	svc.SetWallet(fx.wallet)
	fx.closer = &Closer{Svc: svc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return fx
}

func (fx *closeFixture) insert(out string, expireAt int64) *store.Order {
	fx.t.Helper()
	o := &store.Order{
		TradeNo: "T" + out, OutTradeNo: out, PID: "1000", PayType: "wxpay",
		Name: "充值", Money: "10.00", AmountFen: 1000,
		NotifyURL: "http://example/n", ReturnURL: "http://example/r",
		PayStatus: store.PayCreated, NotifyStatus: store.NotifyNone,
		CreatedAt: expireAt - 7200,
	}
	if err := fx.st.Insert(o); err != nil {
		fx.t.Fatal(err)
	}
	if err := fx.st.SetExpireAt(o.TradeNo, expireAt); err != nil {
		fx.t.Fatal(err)
	}
	if err := fx.st.UpdateJeepay(o.TradeNo, "P1", "codeUrl", "weixin://pay", fx.state); err != nil {
		fx.t.Fatal(err)
	}
	return o
}

func (fx *closeFixture) tick(unix int64) {
	fx.t.Helper()
	fx.closer.Tick(time.Unix(unix, 0))
}

func writeSigned(w http.ResponseWriter, data map[string]string, secret string) {
	body := map[string]any{}
	for k, v := range data {
		if v == "" {
			continue
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && (k == "state" || k == "amount") {
			body[k] = n
			continue
		}
		body[k] = v
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0, "msg": "SUCCESS", "data": body, "sign": jeepay.Sign(data, secret),
	})
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func appService(st *store.Store) *Service {
	return New(Config{
		PublicBaseURL:   "http://127.0.0.1",
		EpayPID:         "1000",
		EpayKey:         "epay-key",
		JeepayBaseURL:   "http://127.0.0.1",
		JeepayMchNo:     "M1",
		JeepayAppID:     "A1",
		JeepayAppSecret: "S",
		WayCodeWxpay:    "WX_NATIVE",
	}, st, nil)
}

func signedEpay(out string) map[string]string {
	p := map[string]string{
		"pid": "1000", "type": "wxpay", "out_trade_no": out,
		"notify_url": "http://example/n", "name": "充值", "money": "10.00",
		"return_url": "http://example/r",
	}
	p["sign"] = epay.Sign(p, "epay-key")
	p["sign_type"] = "MD5"
	return p
}
