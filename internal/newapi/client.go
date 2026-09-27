package newapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrRejected 表示 NewAPI 明确拒绝，余额没有变化。
var ErrRejected = errors.New("NewAPI 拒绝扣余额")

// ErrUnauthenticated 表示调用者的登录状态无效。
var ErrUnauthenticated = errors.New("NewAPI 登录无效")

// ErrUncertain 表示请求没有明确结果，余额可能已经变化。
var ErrUncertain = errors.New("NewAPI 扣余额结果不明")

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    httpClient,
	}
}

type Billing struct {
	QuotaPerUnit float64
	DisplayType  string
}

func (c *Client) Billing(ctx context.Context) (Billing, error) {
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			QuotaPerUnit float64 `json:"quota_per_unit"`
			DisplayType  string  `json:"quota_display_type"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/api/status", &body); err != nil {
		return Billing{}, err
	}
	if !body.Success || body.Data.QuotaPerUnit <= 0 || body.Data.DisplayType == "" {
		return Billing{}, fmt.Errorf("NewAPI 额度设置无效")
	}
	return Billing{QuotaPerUnit: body.Data.QuotaPerUnit, DisplayType: body.Data.DisplayType}, nil
}

// Subtract 按额度整数扣减。value 是额度，不是美元。
func (c *Client) Subtract(ctx context.Context, userID, quota int) error {
	if c.Token == "" {
		return fmt.Errorf("未配置 NewAPI 管理令牌")
	}
	payload, err := json.Marshal(map[string]any{
		"id":     userID,
		"action": "add_quota",
		"mode":   "subtract",
		"value":  quota,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/user/manage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUncertain, err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUncertain, err.Error())
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: HTTP %d", ErrUncertain, resp.StatusCode)
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("%w: 响应不是 JSON", ErrUncertain)
	}
	if resp.StatusCode != http.StatusOK || !body.Success {
		msg := strings.TrimSpace(body.Message)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("%w: %s", ErrRejected, msg)
	}
	return nil
}

type subtractLog struct {
	UserID    int    `json:"user_id"`
	CreatedAt int64  `json:"created_at"`
	Content   string `json:"content"`
}

// HasSubtract 查看目标用户在 since 之后是否已有一笔相同额度的扣减记录。
func (c *Client) HasSubtract(ctx context.Context, userID, quota int, since int64) (bool, error) {
	if c.Token == "" {
		return false, fmt.Errorf("未配置 NewAPI 管理令牌")
	}
	url := fmt.Sprintf("%s/api/log/?type=1&start_timestamp=%d&p=1&page_size=50", c.BaseURL, since)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Items []subtractLog `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || resp.StatusCode != http.StatusOK || !body.Success {
		return false, fmt.Errorf("读取扣减记录失败")
	}
	want := fmt.Sprintf("Decreased user quota by %d", quota)
	for _, item := range body.Data.Items {
		if item.UserID == userID && item.CreatedAt >= since && item.Content == want {
			return true, nil
		}
	}
	return false, nil
}

// SessionUser 用调用者自己的登录状态读取用户编号，不使用管理令牌。
func (c *Client) SessionUser(ctx context.Context, authorization, cookie string) (int, error) {
	authorization = strings.TrimSpace(authorization)
	cookie = strings.TrimSpace(cookie)
	if authorization == "" && cookie == "" {
		return 0, ErrUnauthenticated
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/user/self", nil)
	if err != nil {
		return 0, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求 NewAPI 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return 0, ErrUnauthenticated
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("NewAPI HTTP %d", resp.StatusCode)
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || !body.Success || body.Data.ID <= 0 {
		return 0, ErrUnauthenticated
	}
	return body.Data.ID, nil
}

func (c *Client) getJSON(ctx context.Context, path string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("请求 NewAPI 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("NewAPI HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("NewAPI 响应不是 JSON")
	}
	return nil
}
