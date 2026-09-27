package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/marco9442/jeepay-epay-adapter/internal/jeepay"
	"github.com/marco9442/jeepay-epay-adapter/internal/newapi"
	"github.com/marco9442/jeepay-epay-adapter/internal/store"
)

const maxWalletQuota = 1<<53 - 1

var (
	userTradeNo = regexp.MustCompile(`^USR(\d+)NO`)
	creditedUSD = regexp.MustCompile(`^TUC(\d+)$`)
)

// HandleRefundNotify 在微信整笔退款成功后，按到账金额扣 NewAPI 余额。
// 到账金额含折扣。部分退款、失败的退款不扣。返回的说明可记日志，不含密钥。
func (s *Service) HandleRefundNotify(ctx context.Context, values url.Values) (string, error) {
	n, err := jeepay.VerifiedRefund(values, s.Cfg.JeepayAppSecret)
	if err != nil {
		return "", err
	}
	if n.MchNo != s.Cfg.JeepayMchNo || n.AppID != s.Cfg.JeepayAppID {
		return "", fmt.Errorf("退款通知商户不匹配")
	}
	if n.State != jeepay.RefundStateSuccess {
		return "退款未成功，不扣余额", nil
	}
	if n.RefundAmount != n.PayAmount {
		return "部分退款，不自动扣余额", nil
	}
	if n.RefundAmount <= 0 {
		return "", fmt.Errorf("退款金额无效")
	}
	order, err := s.store.ByPayOrderID(n.PayOrderID)
	if err != nil {
		return "", err
	}
	if order.PayStatus != store.PayPaid {
		return "", fmt.Errorf("原订单尚未入账")
	}
	if order.AmountFen != n.PayAmount {
		return "", fmt.Errorf("退款金额与原订单不一致")
	}
	userID, dollars, err := creditedUser(order.OutTradeNo, order.Name)
	if err != nil {
		return "", err
	}

	s.refundMu.Lock()
	defer s.refundMu.Unlock()

	if s.quota == nil || s.Cfg.NewAPIAdminToken == "" {
		return "", fmt.Errorf("未配置 NewAPI 管理令牌")
	}
	billing, err := s.quota.Billing(ctx)
	if err != nil {
		return "", err
	}
	quota, err := creditedQuota(dollars, billing)
	if err != nil {
		return "", err
	}
	claim, err := s.store.ClaimRefund(n.RefundOrderID, n.PayOrderID, userID, quota)
	if err != nil {
		return "", err
	}
	switch claim {
	case store.RefundDone, store.RefundPayDone:
		return "这笔到账额度已经扣过", nil
	case store.RefundPending:
		return s.finishPendingRefund(ctx, n.RefundOrderID)
	case store.RefundNew:
		return s.subtractRefund(ctx, n.RefundOrderID, userID, quota, dollars)
	default:
		return "", fmt.Errorf("退款扣减状态无效")
	}
}

func (s *Service) finishPendingRefund(ctx context.Context, refundID string) (string, error) {
	row, err := s.store.RefundDebit(refundID)
	if err != nil {
		return "", err
	}
	found, err := s.quota.HasSubtract(ctx, row.UserID, row.Quota, row.CreatedAt-1)
	if err != nil {
		return "", err
	}
	if found {
		if err := s.store.FinishRefund(refundID); err != nil {
			return "", err
		}
		return "已确认扣过到账额度", nil
	}
	if time.Since(time.Unix(row.CreatedAt, 0)) < 30*time.Second {
		return "", fmt.Errorf("退款扣余额仍在处理")
	}
	return s.subtractRefund(ctx, refundID, row.UserID, row.Quota, 0)
}

func (s *Service) subtractRefund(ctx context.Context, refundID string, userID, quota int, dollars int64) (string, error) {
	err := s.quota.Subtract(ctx, userID, quota)
	if err != nil {
		if errors.Is(err, newapi.ErrRejected) {
			_ = s.store.ReleaseRefund(refundID)
		}
		return "", err
	}
	if err := s.store.FinishRefund(refundID); err != nil {
		return "", err
	}
	if dollars > 0 {
		return fmt.Sprintf("已扣到账 %d 美元", dollars), nil
	}
	return "已扣到账额度", nil
}

func creditedUser(outTradeNo, name string) (int, int64, error) {
	um := userTradeNo.FindStringSubmatch(outTradeNo)
	nm := creditedUSD.FindStringSubmatch(name)
	if um == nil || nm == nil {
		return 0, 0, fmt.Errorf("订单里没有到账用户或金额")
	}
	userID, err := strconv.Atoi(um[1])
	if err != nil || userID <= 0 {
		return 0, 0, fmt.Errorf("订单用户无效")
	}
	dollars, err := strconv.ParseInt(nm[1], 10, 64)
	if err != nil || dollars <= 0 {
		return 0, 0, fmt.Errorf("到账金额无效")
	}
	return userID, dollars, nil
}

func creditedQuota(dollars int64, billing newapi.Billing) (int, error) {
	var q int64
	switch billing.DisplayType {
	case "TOKENS":
		q = dollars
	case "USD", "CNY", "CUSTOM":
		product := decimal.NewFromInt(dollars).Mul(decimal.NewFromFloat(billing.QuotaPerUnit)).Round(0)
		q = product.IntPart()
	default:
		return 0, fmt.Errorf("未知的额度展示方式")
	}
	if q <= 0 || q > maxWalletQuota || q > math.MaxInt {
		return 0, fmt.Errorf("到账额度超出可扣范围")
	}
	return int(q), nil
}
