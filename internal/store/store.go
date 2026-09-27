package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("订单不存在")
var ErrConflict = errors.New("商户订单号已存在")

const (
	PayCreated = "created"
	PayPaid    = "paid"
	PayFailed  = "failed"

	NotifyNone      = "none"
	NotifyPending   = "pending"
	NotifySucceeded = "succeeded"
	NotifyFailed    = "failed"
)

type Order struct {
	ID               int64
	TradeNo          string
	OutTradeNo       string
	PID              string
	PayType          string
	Name             string
	Money            string
	AmountFen        int64
	NotifyURL        string
	ReturnURL        string
	JeepayPayOrderID string
	JeepayState      int
	PayDataType      string
	PayData          string
	PayStatus        string
	NotifyStatus     string
	NotifyAttempts   int
	NextNotifyAt     int64
	CreatedAt        int64
	PaidAt           int64
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS orders (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trade_no TEXT NOT NULL UNIQUE,
  out_trade_no TEXT NOT NULL,
  pid TEXT NOT NULL,
  pay_type TEXT NOT NULL,
  name TEXT NOT NULL,
  money TEXT NOT NULL,
  amount_fen INTEGER NOT NULL,
  notify_url TEXT NOT NULL,
  return_url TEXT NOT NULL,
  jeepay_pay_order_id TEXT NOT NULL DEFAULT '',
  jeepay_state INTEGER NOT NULL DEFAULT 0,
  pay_data_type TEXT NOT NULL DEFAULT '',
  pay_data TEXT NOT NULL DEFAULT '',
  pay_status TEXT NOT NULL DEFAULT 'created',
  notify_status TEXT NOT NULL DEFAULT 'none',
  notify_attempts INTEGER NOT NULL DEFAULT 0,
  next_notify_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  paid_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE(pid, out_trade_no)
);
CREATE INDEX IF NOT EXISTS idx_orders_notify ON orders(notify_status, next_notify_at);
CREATE TABLE IF NOT EXISTS refund_debits (
  refund_order_id TEXT PRIMARY KEY,
  pay_order_id TEXT NOT NULL,
  user_id INTEGER NOT NULL,
  quota INTEGER NOT NULL,
  state TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_refund_pay_done ON refund_debits(pay_order_id) WHERE state = 'done';
`)
	return err
}

func (s *Store) Insert(o *Order) error {
	if o.CreatedAt == 0 {
		o.CreatedAt = time.Now().Unix()
	}
	res, err := s.db.Exec(`
INSERT INTO orders (
  trade_no, out_trade_no, pid, pay_type, name, money, amount_fen,
  notify_url, return_url, jeepay_pay_order_id, jeepay_state, pay_data_type, pay_data,
  pay_status, notify_status, notify_attempts, next_notify_at, created_at, paid_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.TradeNo, o.OutTradeNo, o.PID, o.PayType, o.Name, o.Money, o.AmountFen,
		o.NotifyURL, o.ReturnURL, o.JeepayPayOrderID, o.JeepayState, o.PayDataType, o.PayData,
		o.PayStatus, o.NotifyStatus, o.NotifyAttempts, o.NextNotifyAt, o.CreatedAt, o.PaidAt,
	)
	if err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return err
	}
	o.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ByTradeNo(tradeNo string) (*Order, error) {
	return s.get(`SELECT `+orderCols+` FROM orders WHERE trade_no=?`, tradeNo)
}

func (s *Store) ByOutTradeNo(pid, outTradeNo string) (*Order, error) {
	return s.get(`SELECT `+orderCols+` FROM orders WHERE pid=? AND out_trade_no=?`, pid, outTradeNo)
}

func (s *Store) ByMchOrderNo(mchOrderNo string) (*Order, error) {
	return s.get(`SELECT `+orderCols+` FROM orders WHERE out_trade_no=?`, mchOrderNo)
}

func (s *Store) ByPayOrderID(payOrderID string) (*Order, error) {
	if payOrderID == "" {
		return nil, ErrNotFound
	}
	return s.get(`SELECT `+orderCols+` FROM orders WHERE jeepay_pay_order_id=?
ORDER BY CASE pay_status WHEN 'paid' THEN 0 ELSE 1 END, id DESC LIMIT 1`, payOrderID)
}

const (
	RefundNew     = "new"
	RefundPending = "pending"
	RefundDone    = "done"
	RefundPayDone = "pay-done"
)

type RefundDebit struct {
	RefundOrderID string
	PayOrderID    string
	UserID        int
	Quota         int
	State         string
	CreatedAt     int64
}

// ClaimRefund 记下这笔退款准备扣余额。同一退款单或同一支付单成功后不会再扣。
func (s *Store) ClaimRefund(refundID, payOrderID string, userID, quota int) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var state string
	err = tx.QueryRow(`SELECT state FROM refund_debits WHERE refund_order_id=?`, refundID).Scan(&state)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return "", err
		}
		if state == RefundDone {
			return RefundDone, nil
		}
		return RefundPending, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM refund_debits WHERE pay_order_id=? AND state=?`, payOrderID, RefundDone).Scan(&done); err != nil {
		return "", err
	}
	if done > 0 {
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return RefundPayDone, nil
	}
	_, err = tx.Exec(`INSERT INTO refund_debits (refund_order_id, pay_order_id, user_id, quota, state, created_at)
VALUES (?,?,?,?,?,?)`, refundID, payOrderID, userID, quota, RefundPending, time.Now().Unix())
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return RefundNew, nil
}

func (s *Store) RefundDebit(refundID string) (*RefundDebit, error) {
	var d RefundDebit
	err := s.db.QueryRow(`SELECT refund_order_id, pay_order_id, user_id, quota, state, created_at
FROM refund_debits WHERE refund_order_id=?`, refundID).Scan(
		&d.RefundOrderID, &d.PayOrderID, &d.UserID, &d.Quota, &d.State, &d.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) FinishRefund(refundID string) error {
	res, err := s.db.Exec(`UPDATE refund_debits SET state=? WHERE refund_order_id=? AND state=?`,
		RefundDone, refundID, RefundPending)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ReleaseRefund(refundID string) error {
	_, err := s.db.Exec(`DELETE FROM refund_debits WHERE refund_order_id=? AND state=?`, refundID, RefundPending)
	return err
}

// RefundedOutTradeNos 返回该用户已整笔退款并扣过余额的充值单号。
func (s *Store) RefundedOutTradeNos(userID int) ([]string, error) {
	rows, err := s.db.Query(`
SELECT o.out_trade_no
FROM refund_debits d
JOIN orders o ON o.jeepay_pay_order_id = d.pay_order_id
WHERE d.user_id=? AND d.state=?
ORDER BY d.created_at DESC, o.out_trade_no`, userID, RefundDone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var tradeNo string
		if err := rows.Scan(&tradeNo); err != nil {
			return nil, err
		}
		if tradeNo != "" {
			out = append(out, tradeNo)
		}
	}
	return out, rows.Err()
}

func (s *Store) UpdateJeepay(tradeNo, payOrderID, payDataType, payData string, state int) error {
	_, err := s.db.Exec(`
UPDATE orders SET jeepay_pay_order_id=?, pay_data_type=?, pay_data=?, jeepay_state=?
WHERE trade_no=?`, payOrderID, payDataType, payData, state, tradeNo)
	return err
}

func (s *Store) MarkPaid(tradeNo string, jeepayPayOrderID string) error {
	now := time.Now().Unix()
	res, err := s.db.Exec(`
UPDATE orders
SET pay_status=?, jeepay_state=2, jeepay_pay_order_id=CASE WHEN ?='' THEN jeepay_pay_order_id ELSE ? END,
    paid_at=CASE WHEN paid_at=0 THEN ? ELSE paid_at END,
    notify_status=CASE WHEN notify_status=? THEN notify_status ELSE ? END,
    next_notify_at=CASE WHEN notify_status=? THEN next_notify_at ELSE ? END
WHERE trade_no=?`,
		PayPaid, jeepayPayOrderID, jeepayPayOrderID, now,
		NotifySucceeded, NotifyPending,
		NotifySucceeded, now,
		tradeNo,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkFailed(tradeNo string) error {
	_, err := s.db.Exec(`UPDATE orders SET pay_status=? WHERE trade_no=?`, PayFailed, tradeNo)
	return err
}

func (s *Store) DueNotifies(limit int, now int64) ([]*Order, error) {
	rows, err := s.db.Query(`
SELECT `+orderCols+` FROM orders
WHERE notify_status=? AND next_notify_at<=?
ORDER BY next_notify_at ASC LIMIT ?`, NotifyPending, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) NotifySucceeded(tradeNo string) error {
	_, err := s.db.Exec(`UPDATE orders SET notify_status=?, next_notify_at=0 WHERE trade_no=?`, NotifySucceeded, tradeNo)
	return err
}

func (s *Store) NotifyRetry(tradeNo string, attempts int, nextAt int64, giveUp bool) error {
	st := NotifyPending
	if giveUp {
		st = NotifyFailed
	}
	_, err := s.db.Exec(`UPDATE orders SET notify_status=?, notify_attempts=?, next_notify_at=? WHERE trade_no=?`,
		st, attempts, nextAt, tradeNo)
	return err
}

const orderCols = `id, trade_no, out_trade_no, pid, pay_type, name, money, amount_fen,
 notify_url, return_url, jeepay_pay_order_id, jeepay_state, pay_data_type, pay_data,
 pay_status, notify_status, notify_attempts, next_notify_at, created_at, paid_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) get(q string, args ...any) (*Order, error) {
	o, err := scanOrder(s.db.QueryRow(q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

func scanOrder(sc rowScanner) (*Order, error) {
	var o Order
	err := sc.Scan(
		&o.ID, &o.TradeNo, &o.OutTradeNo, &o.PID, &o.PayType, &o.Name, &o.Money, &o.AmountFen,
		&o.NotifyURL, &o.ReturnURL, &o.JeepayPayOrderID, &o.JeepayState, &o.PayDataType, &o.PayData,
		&o.PayStatus, &o.NotifyStatus, &o.NotifyAttempts, &o.NextNotifyAt, &o.CreatedAt, &o.PaidAt,
	)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique")
}
