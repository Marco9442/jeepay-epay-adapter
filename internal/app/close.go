package app

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/marco9442/jeepay-epay-adapter/internal/jeepay"
	"github.com/marco9442/jeepay-epay-adapter/internal/store"
)

const (
	closeLeadSec    = 90
	channelGraceSec = 120
	// missedGraceSec 盖住当前 Jeepay 镜像（241af520）的商户通知。
	// 最多 6 次；失败后按 30/60/90/120/150 秒再发。RocketMQ 把 90 和 150 对齐到 2 分钟和 3 分钟。
	// 再加上每次最多 20 秒 HTTP，最坏大约 630 秒。1800 秒是这个窗口再加余量，不是环境变量。
	missedGraceSec = 1800
	stuckSlackSec  = 600
	closeBatch     = 20
	closeEvery     = 30 * time.Second
)

const (
	walletExpired = "expired"
	walletFailed  = "failed"
	walletSuccess = "success"
	walletNotEpay = "not_epay"
)

// Closer 在通知循环旁边关单。不在请求路径上，也不占用 SQLite 连接去等 HTTP。
type Closer struct {
	Svc   *Service
	Log   *slog.Logger
	Every time.Duration
	Now   func() time.Time
}

func (c *Closer) Run(ctx context.Context) {
	every := c.Every
	if every <= 0 {
		every = closeEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	c.Tick(c.now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Tick(c.now())
		}
	}
}

func (c *Closer) Tick(now time.Time) {
	if c.Svc == nil || c.Svc.store == nil {
		return
	}
	log := c.log()
	unix := now.Unix()
	orders, err := c.Svc.store.DueCloses(unix+closeLeadSec, closeBatch)
	if err != nil {
		log.Error("拉取待关单失败", "err", err)
		return
	}
	for _, o := range orders {
		c.one(unix, o)
	}
	n, err := c.Svc.store.StuckCloseCount(unix, missedGraceSec+stuckSlackSec)
	if err != nil {
		log.Error("统计未结束订单失败", "err", err)
		return
	}
	if n > 0 {
		log.Warn("有订单过了通知宽限仍未结束", "count", n)
	}
}

func (c *Closer) one(now int64, o *store.Order) {
	step := closeStepOf(o, now)
	if step == stepNone || step == stepWait {
		return
	}
	if c.Svc.jeepay == nil {
		return
	}
	q, err := c.Svc.jeepay.QueryByMchOrderNo(o.OutTradeNo)
	if err != nil {
		c.log().Warn("关单前查单失败", "trade", o.TradeNo, "err", err)
		return
	}
	plan := decideQuery(q.State, q.AmountFen, o.AmountFen, step == stepRequery, now, o.ExpireAt)
	if plan.markPaid {
		if err := c.Svc.store.MarkPaid(o.TradeNo, q.PayOrderID); err != nil {
			c.log().Error("记下已支付失败", "trade", o.TradeNo, "err", err)
		}
		return
	}
	if plan.block {
		c.block(o.TradeNo)
		return
	}
	if plan.callClose {
		c.applyClose(now, o, plan)
		return
	}
	if plan.expire {
		c.expireWallet(o.TradeNo)
	}
}

func (c *Closer) applyClose(now int64, o *store.Order, plan queryPlan) {
	out, err := c.Svc.jeepay.CloseByMchOrderNo(o.OutTradeNo)
	if err != nil {
		c.log().Warn("关单请求失败", "trade", o.TradeNo, "err", err)
		if plan.ing || !plan.expire {
			return
		}
		c.expireWallet(o.TradeNo)
		return
	}
	record, block, expire := decideClose(out.Kind, plan.ing, now, o.ExpireAt)
	if block {
		c.block(o.TradeNo)
		return
	}
	if record {
		if err := c.Svc.store.SetChannelClosed(o.TradeNo, now); err != nil {
			c.log().Error("记下渠道已关失败", "trade", o.TradeNo, "err", err)
		}
		return
	}
	if expire {
		c.expireWallet(o.TradeNo)
	}
}

func (c *Closer) block(tradeNo string) {
	if err := c.Svc.store.BlockExpire(tradeNo); err != nil {
		c.log().Error("停止自动关单失败", "trade", tradeNo, "err", err)
	}
}

func (c *Closer) expireWallet(tradeNo string) {
	fresh, err := c.Svc.store.ByTradeNo(tradeNo)
	if err != nil || fresh.PayStatus != store.PayCreated || fresh.ExpireBlocked {
		return
	}
	if c.Svc.wallet == nil {
		c.log().Warn("钱包关单未就绪", "trade", tradeNo)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := c.Svc.wallet.Expire(ctx, fresh.OutTradeNo)
	if err != nil {
		c.log().Warn("钱包关单失败", "trade", tradeNo, "err", err)
		return
	}
	switch result {
	case walletExpired, walletFailed:
		if _, err := c.Svc.store.MarkFailedIfCreated(fresh.TradeNo); err != nil {
			c.log().Error("记下未支付失败", "trade", tradeNo, "err", err)
		}
	case walletSuccess:
		if err := c.Svc.store.MarkPaid(fresh.TradeNo, fresh.JeepayPayOrderID); err != nil {
			c.log().Error("记下已支付失败", "trade", tradeNo, "err", err)
		}
	case walletNotEpay:
		c.block(fresh.TradeNo)
	default:
		c.log().Warn("钱包关单未确认", "trade", tradeNo, "result", result)
	}
}

func (c *Closer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Closer) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type closeStep int

const (
	stepNone closeStep = iota
	stepWait
	stepQuery
	stepRequery
)

func closeStepOf(o *store.Order, now int64) closeStep {
	if o == nil || o.PayStatus != store.PayCreated || o.ExpireBlocked || o.ExpireAt == 0 {
		return stepNone
	}
	if o.ChannelClosedAt > 0 {
		if now < o.ChannelClosedAt+channelGraceSec {
			return stepWait
		}
		return stepRequery
	}
	if now >= o.ExpireAt-closeLeadSec {
		return stepQuery
	}
	return stepNone
}

type queryPlan struct {
	markPaid  bool
	block     bool
	callClose bool
	ing       bool
	expire    bool
}

func decideQuery(state int, amountFen, orderFen int64, requery bool, now, expireAt int64) queryPlan {
	if state == jeepay.StateSuccess {
		if amountFen == orderFen && orderFen > 0 {
			return queryPlan{markPaid: true}
		}
		return queryPlan{block: true}
	}
	if requery {
		return queryPlan{expire: true}
	}
	switch state {
	case jeepay.StateIng:
		return queryPlan{callClose: true, ing: true}
	case jeepay.StateInit:
		return queryPlan{callClose: true, expire: missedDue(now, expireAt)}
	case jeepay.StateClosed, 3, 4, 5:
		if missedDue(now, expireAt) {
			return queryPlan{expire: true}
		}
		return queryPlan{}
	default:
		return queryPlan{}
	}
}

func decideClose(kind string, ing bool, now, expireAt int64) (recordClosed, block, expire bool) {
	switch kind {
	case jeepay.ClosePaid:
		return false, true, false
	case jeepay.CloseSuccess:
		if ing {
			return true, false, false
		}
		return false, false, missedDue(now, expireAt)
	case jeepay.CloseRejected:
		return false, false, missedDue(now, expireAt)
	case jeepay.CloseFailed:
		if ing {
			return false, false, false
		}
		return false, false, missedDue(now, expireAt)
	default:
		if ing {
			return false, false, false
		}
		return false, false, missedDue(now, expireAt)
	}
}

func missedDue(now, expireAt int64) bool {
	return expireAt > 0 && now >= expireAt+missedGraceSec
}
