package jeepay

import (
	"net/url"
	"testing"
)

func TestVerifiedNotify(t *testing.T) {
	t.Parallel()
	params := map[string]string{
		"payOrderId": "P1",
		"mchOrderNo": "OUT1",
		"amount":     "1000",
		"state":      "2",
		"wayCode":    "WX_NATIVE",
	}
	params["sign"] = Sign(params, "secret")
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	n, err := VerifiedNotify(form, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if n.PayOrderID != "P1" || n.MchOrderNo != "OUT1" || n.AmountFen != 1000 || n.State != 2 {
		t.Fatalf("%+v", n)
	}
	if _, err := VerifiedNotify(form, "other"); err == nil {
		t.Fatal("错密钥应失败")
	}
}
