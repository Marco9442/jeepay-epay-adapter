package epay

import (
	"crypto/md5"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	StatusTradeSuccess = "TRADE_SUCCESS"
	SignTypeMD5        = "MD5"
)

// Sign 按 go-epay / 易支付 V1：去掉 sign、sign_type 和空值，ASCII 排序，md5(k=v&... + key) 小写。
func Sign(params map[string]string, key string) string {
	filtered := filter(params)
	keys := make([]string, 0, len(filtered))
	for k := range filtered {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(filtered[k])
	}
	sum := md5.Sum([]byte(b.String() + key))
	return fmt.Sprintf("%x", sum)
}

func Verify(params map[string]string, key string) bool {
	got := params["sign"]
	if got == "" {
		return false
	}
	return strings.EqualFold(got, Sign(params, key))
}

func filter(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// NotifyValues 组装易支付异步通知/同步跳转字段。money 必须用下单时的原始字符串。
func NotifyValues(pid, tradeNo, outTradeNo, payType, name, money string, key string) url.Values {
	params := map[string]string{
		"pid":          pid,
		"trade_no":     tradeNo,
		"out_trade_no": outTradeNo,
		"type":         payType,
		"name":         name,
		"money":        money,
		"trade_status": StatusTradeSuccess,
	}
	params["sign"] = Sign(params, key)
	params["sign_type"] = SignTypeMD5
	v := url.Values{}
	for k, val := range params {
		v.Set(k, val)
	}
	return v
}
