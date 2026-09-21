package jeepay

import (
	"encoding/json"
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

func TestVerifyDataSignRejectsBadSign(t *testing.T) {
	t.Parallel()
	err := verifyDataSign(map[string]any{"mchOrderNo": "x"}, "DEADBEEF", "secret")
	if err == nil {
		t.Fatal("期望验签失败")
	}
}
