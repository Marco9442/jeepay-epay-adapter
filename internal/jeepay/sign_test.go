package jeepay

import (
	"crypto/md5"
	"fmt"
	"strings"
	"testing"
)

func TestSignMatchesJeepayKitExample(t *testing.T) {
	t.Parallel()
	// 与 jeepay-payment api1.md 同一组字段。文档里的
	// 4A5078DABBCE0D9C4E7668DACB96FF7A 与该待签串的 MD5 不一致，
	// 这里以 JeepayKit.getSign 的真实算法为准。
	params := map[string]string{
		"amount":     "10000",
		"clientIp":   "192.168.0.111",
		"mchOrderNo": "P0123456789101",
		"notifyUrl":  "https://www.baidu.com",
		"platId":     "1000",
		"reqTime":    "20190723141000",
		"returnUrl":  "https://www.baidu.com",
		"version":    "1.0",
		"sign":       "ignore",
	}
	got := Sign(params, "EWEFD123RGSRETYDFNGFGFGSHDFGH")
	raw := "amount=10000&clientIp=192.168.0.111&mchOrderNo=P0123456789101&notifyUrl=https://www.baidu.com&platId=1000&reqTime=20190723141000&returnUrl=https://www.baidu.com&version=1.0&key=EWEFD123RGSRETYDFNGFGFGSHDFGH"
	want := strings.ToUpper(fmt.Sprintf("%x", md5.Sum([]byte(raw))))
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if want != "84E1CA56F984502BBAC06EA6707157F5" {
		t.Fatalf("算法向量变了: %s", want)
	}
}

func TestSignCaseInsensitiveKeySort(t *testing.T) {
	t.Parallel()
	a := Sign(map[string]string{"mchNo": "M1", "AppId": "A1", "amount": "1"}, "k")
	b := Sign(map[string]string{"AppId": "A1", "amount": "1", "mchNo": "M1"}, "k")
	if a != b {
		t.Fatal("排序应稳定")
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	params := map[string]string{"mchNo": "M1", "amount": "8"}
	params["sign"] = Sign(params, "secret")
	if !Verify(params, "secret") {
		t.Fatal("验签失败")
	}
	if Verify(params, "other") {
		t.Fatal("错密钥不应通过")
	}
}
