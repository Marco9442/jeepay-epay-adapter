package jeepay

import (
	"crypto/md5"
	"fmt"
	"sort"
	"strings"
)

// Sign 对齐 JeepayKit.getSign：非空字段按键名忽略大小写排序，
// 每段为 key=value&，末尾再拼 key=密钥，MD5 大写。
func Sign(params map[string]string, key string) string {
	type kv struct{ k, v string }
	list := make([]kv, 0, len(params))
	for k, v := range params {
		if k == "sign" || v == "" {
			continue
		}
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].k) < strings.ToLower(list[j].k)
	})
	var b strings.Builder
	for _, item := range list {
		b.WriteString(item.k)
		b.WriteByte('=')
		b.WriteString(item.v)
		b.WriteByte('&')
	}
	b.WriteString("key=")
	b.WriteString(key)
	sum := md5.Sum([]byte(b.String()))
	return strings.ToUpper(fmt.Sprintf("%x", sum))
}

func Verify(params map[string]string, key string) bool {
	got := params["sign"]
	if got == "" {
		return false
	}
	return strings.EqualFold(got, Sign(params, key))
}
