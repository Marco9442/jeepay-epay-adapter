package epay

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// YuanToFen 把易支付的元字符串转成分。必须是最多两位小数的正数，避免浮点误差。
func YuanToFen(yuan string) (int64, error) {
	s := strings.TrimSpace(yuan)
	if s == "" {
		return 0, fmt.Errorf("金额为空")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return 0, fmt.Errorf("金额格式错误")
	}
	if d.LessThanOrEqual(decimal.Zero) {
		return 0, fmt.Errorf("金额必须大于 0")
	}
	fen := d.Mul(decimal.NewFromInt(100))
	if !fen.Equal(fen.Truncate(0)) {
		return 0, fmt.Errorf("金额不能超过分")
	}
	return fen.IntPart(), nil
}
