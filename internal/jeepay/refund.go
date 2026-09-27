package jeepay

import (
	"fmt"
	"net/url"
	"strconv"
)

const RefundStateSuccess = 2

type RefundNotify struct {
	RefundOrderID string
	PayOrderID    string
	MchNo         string
	AppID         string
	PayAmount     int64
	RefundAmount  int64
	State         int
}

// VerifiedRefund 解析并验签 Jeepay 退款商户通知。
func VerifiedRefund(values url.Values, secret string) (RefundNotify, error) {
	params := mapForm(values)
	if !Verify(params, secret) {
		return RefundNotify{}, fmt.Errorf("Jeepay 退款通知验签失败")
	}
	payAmount, err := strconv.ParseInt(params["payAmount"], 10, 64)
	if err != nil {
		return RefundNotify{}, fmt.Errorf("原支付金额无效")
	}
	refundAmount, err := strconv.ParseInt(params["refundAmount"], 10, 64)
	if err != nil {
		return RefundNotify{}, fmt.Errorf("退款金额无效")
	}
	state, err := strconv.Atoi(params["state"])
	if err != nil {
		return RefundNotify{}, fmt.Errorf("退款状态无效")
	}
	n := RefundNotify{
		RefundOrderID: params["refundOrderId"],
		PayOrderID:    params["payOrderId"],
		MchNo:         params["mchNo"],
		AppID:         params["appId"],
		PayAmount:     payAmount,
		RefundAmount:  refundAmount,
		State:         state,
	}
	if n.RefundOrderID == "" || n.PayOrderID == "" {
		return RefundNotify{}, fmt.Errorf("退款通知缺少单号")
	}
	return n, nil
}
