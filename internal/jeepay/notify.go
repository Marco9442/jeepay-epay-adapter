package jeepay

import (
	"fmt"
	"net/url"
	"strconv"
)

type NotifyOrder struct {
	PayOrderID string
	MchOrderNo string
	AmountFen  int64
	State      int
}

// VerifiedNotify 解析并验签 Jeepay 商户通知（POST form 或 URL query）。
func VerifiedNotify(values url.Values, secret string) (NotifyOrder, error) {
	params := mapForm(values)
	if !Verify(params, secret) {
		return NotifyOrder{}, fmt.Errorf("Jeepay 验签失败")
	}
	fen, err := strconv.ParseInt(params["amount"], 10, 64)
	if err != nil {
		return NotifyOrder{}, fmt.Errorf("通知金额无效")
	}
	state, err := strconv.Atoi(params["state"])
	if err != nil {
		return NotifyOrder{}, fmt.Errorf("通知状态无效")
	}
	return NotifyOrder{
		PayOrderID: params["payOrderId"],
		MchOrderNo: params["mchOrderNo"],
		AmountFen:  fen,
		State:      state,
	}, nil
}

func mapForm(values url.Values) map[string]string {
	out := make(map[string]string, len(values))
	for k, vs := range values {
		if len(vs) == 0 {
			continue
		}
		out[k] = vs[0]
	}
	return out
}
