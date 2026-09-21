package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marco9442/jeepay-epay-adapter/internal/epay"
	"github.com/marco9442/jeepay-epay-adapter/internal/jeepay"
	"github.com/marco9442/jeepay-epay-adapter/internal/store"
)

type Service struct {
	Cfg    Config
	store  *store.Store
	jeepay *jeepay.Client
}

func New(cfg Config, st *store.Store, httpClient *http.Client) *Service {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Service{
		Cfg:    cfg,
		store:  st,
		jeepay: jeepay.New(cfg.JeepayBaseURL, cfg.JeepayMchNo, cfg.JeepayAppID, cfg.JeepayAppSecret, httpClient),
	}
}

// Submit 校验易支付下单并转给 Jeepay，返回收银台地址。
func (s *Service) Submit(params map[string]string, clientIP string) (string, error) {
	if params["pid"] != s.Cfg.EpayPID {
		return "", fmt.Errorf("商户不存在")
	}
	if !epay.Verify(params, s.Cfg.EpayKey) {
		return "", fmt.Errorf("签名校验失败")
	}
	outTradeNo := strings.TrimSpace(params["out_trade_no"])
	if outTradeNo == "" {
		return "", fmt.Errorf("缺少 out_trade_no")
	}
	wayCode, err := s.Cfg.WayCodeFor(params["type"])
	if err != nil {
		return "", err
	}
	fen, err := epay.YuanToFen(params["money"])
	if err != nil {
		return "", err
	}

	exist, err := s.store.ByOutTradeNo(params["pid"], outTradeNo)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if exist != nil && (exist.PayStatus == store.PayPaid || exist.PayData != "") {
		return s.payPageURL(exist), nil
	}

	name := params["name"]
	if name == "" {
		name = "充值"
	}
	order := exist
	if order == nil {
		order = &store.Order{
			TradeNo:      newTradeNo(),
			OutTradeNo:   outTradeNo,
			PID:          params["pid"],
			PayType:      params["type"],
			Name:         name,
			Money:        strings.TrimSpace(params["money"]),
			AmountFen:    fen,
			NotifyURL:    params["notify_url"],
			ReturnURL:    params["return_url"],
			PayStatus:    store.PayCreated,
			NotifyStatus: store.NotifyNone,
		}
		if err := s.store.Insert(order); err != nil {
			if errors.Is(err, store.ErrConflict) {
				dup, err := s.store.ByOutTradeNo(params["pid"], outTradeNo)
				if err != nil {
					return "", err
				}
				return s.payPageURL(dup), nil
			}
			return "", err
		}
	}

	clientIP = strings.TrimSpace(clientIP)
	if clientIP == "" {
		clientIP = "127.0.0.1"
	}
	resp, err := s.jeepay.UnifiedOrder(jeepay.UnifiedOrderReq{
		MchOrderNo:   order.OutTradeNo,
		WayCode:      wayCode,
		AmountFen:    order.AmountFen,
		Subject:      order.Name,
		Body:         order.Name,
		NotifyURL:    s.Cfg.InternalBaseURL + "/jeepay/notify",
		ReturnURL:    s.payPageURL(order),
		ClientIP:     clientIP,
		ChannelExtra: `{"payDataType":"codeUrl"}`,
	})
	if err != nil {
		if recovered, recErr := s.recoverExistingJeepay(order); recErr == nil {
			return s.payPageURL(recovered), nil
		}
		_ = s.store.MarkFailed(order.TradeNo)
		return "", fmt.Errorf("上游下单失败: %w", err)
	}
	if err := s.store.UpdateJeepay(order.TradeNo, resp.PayOrderID, resp.PayDataType, resp.PayData, resp.OrderState); err != nil {
		return "", err
	}
	order.JeepayPayOrderID = resp.PayOrderID
	order.PayDataType = resp.PayDataType
	order.PayData = resp.PayData
	order.JeepayState = resp.OrderState
	if resp.OrderState == jeepay.StateSuccess {
		_ = s.store.MarkPaid(order.TradeNo, resp.PayOrderID)
		order.PayStatus = store.PayPaid
	} else {
		order.PayStatus = store.PayCreated
	}
	return s.payPageURL(order), nil
}

// recoverExistingJeepay 处理 Jeepay 已受理但本地尚未记下二维码的重复下单。
func (s *Service) recoverExistingJeepay(order *store.Order) (*store.Order, error) {
	q, err := s.jeepay.QueryByMchOrderNo(order.OutTradeNo)
	if err != nil {
		return nil, err
	}
	if q.PayOrderID == "" {
		return nil, fmt.Errorf("上游无此单")
	}
	if err := s.store.UpdateJeepay(order.TradeNo, q.PayOrderID, order.PayDataType, order.PayData, q.State); err != nil {
		return nil, err
	}
	order.JeepayPayOrderID = q.PayOrderID
	order.JeepayState = q.State
	if q.State == jeepay.StateSuccess {
		_ = s.store.MarkPaid(order.TradeNo, q.PayOrderID)
		order.PayStatus = store.PayPaid
		return order, nil
	}
	if order.PayData == "" {
		return nil, fmt.Errorf("上游订单已存在但缺少支付串")
	}
	return order, nil
}

func (s *Service) HandleJeepayNotify(values url.Values) error {
	n, err := jeepay.VerifiedNotify(values, s.Cfg.JeepayAppSecret)
	if err != nil {
		return err
	}
	order, err := s.store.ByMchOrderNo(n.MchOrderNo)
	if err != nil {
		return err
	}
	if n.AmountFen != order.AmountFen {
		return fmt.Errorf("通知金额与订单不一致")
	}
	if n.State == jeepay.StateSuccess {
		return s.store.MarkPaid(order.TradeNo, n.PayOrderID)
	}
	return nil
}

// Cashier 是收银台只需要的视图，HTTP 层不必再碰订单存储。
type Cashier struct {
	TradeNo   string
	Money     string
	PayData   string
	ReturnURL string
	QRContent string
	State     string
}

func (c Cashier) Paid() bool { return c.State == store.PayPaid }

func (s *Service) Cashier(tradeNo string) (Cashier, error) {
	o, err := s.store.ByTradeNo(tradeNo)
	if err != nil {
		return Cashier{}, err
	}
	payData := o.PayData
	qr := o.PayData
	if payData == "" {
		payData = "#"
		qr = s.payPageURL(o)
	}
	return Cashier{
		TradeNo:   o.TradeNo,
		Money:     o.Money,
		PayData:   payData,
		ReturnURL: s.returnURL(o),
		QRContent: qr,
		State:     o.PayStatus,
	}, nil
}

func (s *Service) payPageURL(order *store.Order) string {
	return s.Cfg.PublicBaseURL + "/pay/" + order.TradeNo
}

func (s *Service) returnURL(order *store.Order) string {
	if order.ReturnURL == "" {
		return ""
	}
	u, err := url.Parse(order.ReturnURL)
	if err != nil {
		return order.ReturnURL
	}
	q := u.Query()
	for k, vs := range s.epayNotify(order) {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Service) epayNotify(o *store.Order) url.Values {
	tradeNo := o.TradeNo
	if o.JeepayPayOrderID != "" {
		tradeNo = o.JeepayPayOrderID
	}
	return epay.NotifyValues(o.PID, tradeNo, o.OutTradeNo, o.PayType, o.Name, o.Money, s.Cfg.EpayKey)
}

func newTradeNo() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("A%s%s", time.Now().UTC().Format("20060102150405"), hex.EncodeToString(b[:]))
}
