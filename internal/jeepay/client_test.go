package jeepay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyDataSignNumericJSON(t *testing.T) {
	t.Parallel()
	secret := "jeepay-secret"
	data := map[string]string{
		"payOrderId":  "P1",
		"mchOrderNo":  "MCH1",
		"orderState":  "1",
		"payDataType": "codeUrl",
		"payData":     "weixin://pay",
	}
	sign := Sign(data, secret)
	rawJSON := `{
		"code": 0,
		"msg": "SUCCESS",
		"data": {
			"payOrderId": "P1",
			"mchOrderNo": "MCH1",
			"orderState": 1,
			"payDataType": "codeUrl",
			"payData": "weixin://pay"
		},
		"sign": "` + sign + `"
	}`
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := unwrap(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyDataSign(got, str(raw["sign"]), secret); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyClose(t *testing.T) {
	t.Parallel()
	if ClassifyClose(0, "SUCCESS") != CloseSuccess {
		t.Fatal("code 0")
	}
	if ClassifyClose(9999, "订单已支付 ORDERPAID") != ClosePaid {
		t.Fatal("ORDERPAID")
	}
	if ClassifyClose(9999, "当前订单不可关闭") != CloseRejected {
		t.Fatal("rejected")
	}
	if ClassifyClose(9999, "系统异常") != CloseFailed {
		t.Fatal("failed")
	}
}

func TestUnifiedOrderExpiredTimeIsNumber(t *testing.T) {
	t.Parallel()
	const secret = "jeepay-secret"
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		data := map[string]string{
			"payOrderId": "P1", "mchOrderNo": "M1", "orderState": "1",
			"payDataType": "codeUrl", "payData": "weixin://pay",
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "SUCCESS", "data": data, "sign": Sign(data, secret),
		})
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "M1", "A1", secret, srv.Client())
	if _, err := c.UnifiedOrder(UnifiedOrderReq{MchOrderNo: "M1", WayCode: "WX_NATIVE", AmountFen: 1000, ExpiredTimeSec: 7200}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rawBody, []byte(`"expiredTime":7200`)) {
		t.Fatalf("body=%s", rawBody)
	}
	if bytes.Contains(rawBody, []byte(`"expiredTime":"`)) {
		t.Fatalf("expiredTime 不能是字符串: %s", rawBody)
	}
}

func TestCloseBadSignIsNotSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": map[string]any{}, "sign": "DEAD"})
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "M1", "A1", "secret", srv.Client())
	out, err := c.CloseByMchOrderNo("M1")
	if err != nil || out.Kind != CloseFailed {
		t.Fatalf("验签失败不能当成关单成功: %+v %v", out, err)
	}
}

func TestVerifyDataSignRejectsBadSign(t *testing.T) {
	t.Parallel()
	err := verifyDataSign(map[string]any{"mchOrderNo": "x"}, "DEADBEEF", "secret")
	if err == nil {
		t.Fatal("期望验签失败")
	}
}
