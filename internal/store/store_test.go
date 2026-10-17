package store

import (
	"path/filepath"
	"testing"
)

func TestBackfillExpireAtOnlyCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	created := &Order{
		TradeNo: "T1", OutTradeNo: "O1", PID: "1000", PayType: "wxpay",
		Name: "充值", Money: "10.00", AmountFen: 1000,
		NotifyURL: "http://example/n", ReturnURL: "http://example/r",
		PayStatus: PayCreated, CreatedAt: 1000,
	}
	paid := &Order{
		TradeNo: "T2", OutTradeNo: "O2", PID: "1000", PayType: "wxpay",
		Name: "充值", Money: "10.00", AmountFen: 1000,
		NotifyURL: "http://example/n", ReturnURL: "http://example/r",
		PayStatus: PayPaid, CreatedAt: 2000,
	}
	if err := st.Insert(created); err != nil {
		t.Fatal(err)
	}
	if err := st.Insert(paid); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	got, err := st.ByTradeNo("T1")
	if err != nil || got.ExpireAt != 1000+7200 {
		t.Fatalf("旧未支付单应补 2 小时: %+v %v", got, err)
	}
	got, err = st.ByTradeNo("T2")
	if err != nil || got.ExpireAt != 0 {
		t.Fatalf("已支付单不补截止时间: %+v %v", got, err)
	}
}
