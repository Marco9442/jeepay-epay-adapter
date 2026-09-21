package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/marco9442/jeepay-epay-adapter/internal/store"
)

// Worker 把已支付订单按易支付协议通知 NewAPI，失败按 15s 起、封顶 10 分钟重试。
type Worker struct {
	Svc    *Service
	HTTP   *http.Client
	Log    *slog.Logger
	Every  time.Duration
	MaxTry int
}

func (w *Worker) Run(ctx context.Context) {
	if w.Every <= 0 {
		w.Every = 5 * time.Second
	}
	if w.MaxTry <= 0 {
		w.MaxTry = 10
	}
	if w.HTTP == nil {
		w.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	t := time.NewTicker(w.Every)
	defer t.Stop()
	w.tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick()
		}
	}
}

func (w *Worker) Tick() { w.tick() }

func (w *Worker) tick() {
	orders, err := w.Svc.store.DueNotifies(20, time.Now().Unix())
	if err != nil {
		w.Log.Error("拉取待通知订单失败", "err", err)
		return
	}
	for _, o := range orders {
		w.send(o)
	}
}

func (w *Worker) send(o *store.Order) {
	if o.NotifyURL == "" {
		_ = w.Svc.store.NotifySucceeded(o.TradeNo)
		return
	}
	form := w.Svc.epayNotify(o)
	resp, err := w.HTTP.Post(o.NotifyURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	body := ""
	if err == nil {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		body = strings.TrimSpace(string(b))
	}
	if err == nil && strings.EqualFold(body, "success") {
		if err := w.Svc.store.NotifySucceeded(o.TradeNo); err != nil {
			w.Log.Error("更新通知成功状态失败", "trade", o.TradeNo, "err", err)
		}
		return
	}
	attempts := o.NotifyAttempts + 1
	giveUp := attempts >= w.MaxTry
	next := time.Now().Add(backoff(attempts)).Unix()
	if giveUp {
		next = 0
	}
	if err := w.Svc.store.NotifyRetry(o.TradeNo, attempts, next, giveUp); err != nil {
		w.Log.Error("更新通知重试失败", "trade", o.TradeNo, "err", err)
	}
	w.Log.Warn("易支付异步通知未成功", "trade", o.TradeNo, "attempt", attempts, "err", err, "body", body)
}

func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := time.Duration(attempt) * 15 * time.Second
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}
