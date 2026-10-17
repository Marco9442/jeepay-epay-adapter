package wallet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config 是关单角色的连接参数。密码只放在连接对象里，不拼进日志。
type Config struct {
	Host string
	Port int
	Name string
	User string
	Pass string
}

// Client 只调用 pay.expire_epay_topup。一条连接，语句超时 2 秒。
type Client struct {
	pool *pgxpool.Pool
	pass string
}

// Unavailable 表示关单库没配好。新下单会拒绝，通知循环仍可运行。
type Unavailable struct{}

func (Unavailable) Ready(context.Context) error { return errors.New("关单未就绪") }

func (Unavailable) Expire(context.Context, string) (string, error) {
	return "", errors.New("关单未就绪")
}

func Open(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Pass) == "" {
		return nil, errors.New("缺少 PAY_EXPIRE_DB_PASSWORD")
	}
	if cfg.Port == 0 {
		cfg.Port = 5432
	}
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s sslmode=disable", cfg.Host, cfg.Port, cfg.Name, cfg.User)
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	poolCfg.ConnConfig.Password = cfg.Pass
	poolCfg.MaxConns = 1
	poolCfg.MinConns = 0
	poolCfg.ConnConfig.ConnectTimeout = 2 * time.Second
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = "2000"
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, redact(err, cfg.Pass)
	}
	return &Client{pool: pool, pass: cfg.Pass}, nil
}

func (c *Client) Close() {
	if c != nil && c.pool != nil {
		c.pool.Close()
	}
}

func (c *Client) Ready(ctx context.Context) error {
	var got string
	err := c.pool.QueryRow(ctx, "SELECT pay.expire_epay_topup($1)", "").Scan(&got)
	if err != nil {
		return redact(err, c.pass)
	}
	if got != "missing" {
		return fmt.Errorf("关单函数探针返回 %s", got)
	}
	return nil
}

func (c *Client) Expire(ctx context.Context, tradeNo string) (string, error) {
	var got string
	err := c.pool.QueryRow(ctx, "SELECT pay.expire_epay_topup($1)", tradeNo).Scan(&got)
	if err != nil {
		return "", redact(err, c.pass)
	}
	return got, nil
}

func redact(err error, pass string) error {
	if err == nil || pass == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), pass, "***"))
}
