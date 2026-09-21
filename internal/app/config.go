package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr string

	PublicBaseURL   string
	InternalBaseURL string

	EpayPID string
	EpayKey string

	JeepayBaseURL   string
	JeepayMchNo     string
	JeepayAppID     string
	JeepayAppSecret string
	WayCodeWxpay    string

	SQLitePath string

	NotifyInterval time.Duration
	NotifyMaxTries int
}

func FromEnv() (Config, error) {
	c := Config{
		HTTPAddr:        env("HTTP_ADDR", ":8080"),
		PublicBaseURL:   strings.TrimRight(env("PUBLIC_BASE_URL", "http://127.0.0.1:8080"), "/"),
		InternalBaseURL: strings.TrimRight(env("INTERNAL_BASE_URL", ""), "/"),
		EpayPID:         env("EPAY_PID", ""),
		EpayKey:         env("EPAY_KEY", ""),
		JeepayBaseURL:   strings.TrimRight(env("JEEPAY_BASE_URL", ""), "/"),
		JeepayMchNo:     env("JEEPAY_MCH_NO", ""),
		JeepayAppID:     env("JEEPAY_APP_ID", ""),
		JeepayAppSecret: env("JEEPAY_APP_SECRET", ""),
		WayCodeWxpay:    env("JEEPAY_WAY_CODE_WXPAY", "WX_NATIVE"),
		SQLitePath:      env("SQLITE_PATH", "/data/adapter.db"),
		NotifyInterval:  time.Duration(envInt("NOTIFY_INTERVAL_SEC", 5)) * time.Second,
		NotifyMaxTries:  envInt("NOTIFY_MAX_TRIES", 10),
	}
	if c.InternalBaseURL == "" {
		c.InternalBaseURL = c.PublicBaseURL
	}
	var missing []string
	for _, item := range []struct{ k, v string }{
		{"EPAY_PID", c.EpayPID},
		{"EPAY_KEY", c.EpayKey},
		{"JEEPAY_BASE_URL", c.JeepayBaseURL},
		{"JEEPAY_MCH_NO", c.JeepayMchNo},
		{"JEEPAY_APP_ID", c.JeepayAppID},
		{"JEEPAY_APP_SECRET", c.JeepayAppSecret},
	} {
		if item.v == "" {
			missing = append(missing, item.k)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("缺少配置: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func (c Config) WayCodeFor(epayType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(epayType)) {
	case "wxpay":
		return c.WayCodeWxpay, nil
	default:
		return "", fmt.Errorf("不支持的支付方式: %s", epayType)
	}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return def
	}
	return n
}
