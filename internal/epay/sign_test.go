package epay

import "testing"

func TestSignMatchesGoEpay(t *testing.T) {
	t.Parallel()
	params := map[string]string{
		"pid":          "1000",
		"type":         "wxpay",
		"out_trade_no": "USR1NOabc1231700000000",
		"notify_url":   "http://new-api:3000/api/user/epay/notify",
		"name":         "TUC10",
		"money":        "10.00",
		"device":       "pc",
		"sign_type":    "MD5",
		"return_url":   "http://localhost:3000/usage-logs",
		"sign":         "",
	}
	key := "test-epay-key"
	sign := Sign(params, key)
	params["sign"] = sign
	if !Verify(params, key) {
		t.Fatal("自签自验失败")
	}
	// device 必须参与签名；漏字段应失败
	bad := clone(params)
	delete(bad, "device")
	bad["sign"] = Sign(map[string]string{
		"pid": params["pid"], "type": params["type"], "out_trade_no": params["out_trade_no"],
		"notify_url": params["notify_url"], "name": params["name"], "money": params["money"],
		"return_url": params["return_url"],
	}, key)
	if Verify(params, key) && bad["sign"] == params["sign"] {
		t.Fatal("漏掉 device 的签名不应与完整签名相同")
	}
	if Sign(params, key) == Sign(without(params, "device"), key) {
		t.Fatal("device 未参与签名")
	}
}

func TestEmptyAndSignTypeIgnored(t *testing.T) {
	t.Parallel()
	a := map[string]string{"money": "1.00", "pid": "1", "sign_type": "MD5", "extra": ""}
	b := map[string]string{"money": "1.00", "pid": "1"}
	if Sign(a, "k") != Sign(b, "k") {
		t.Fatal("空值和 sign_type 应被忽略")
	}
}

func clone(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func without(in map[string]string, key string) map[string]string {
	out := clone(in)
	delete(out, key)
	return out
}
