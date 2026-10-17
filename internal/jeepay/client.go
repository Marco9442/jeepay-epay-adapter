package jeepay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	StateInit      = 0
	StateIng       = 1
	StateSuccess   = 2
	StateClosed    = 6
	PayDataCodeURL = "codeUrl"

	CloseSuccess  = "success"
	ClosePaid     = "paid"
	CloseRejected = "rejected"
	CloseFailed   = "failed"
)

type Client struct {
	BaseURL   string
	MchNo     string
	AppID     string
	AppSecret string
	HTTP      *http.Client
}

func New(baseURL, mchNo, appID, secret string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		MchNo:     mchNo,
		AppID:     appID,
		AppSecret: secret,
		HTTP:      httpClient,
	}
}

type UnifiedOrderReq struct {
	MchOrderNo     string
	WayCode        string
	AmountFen      int64
	Currency       string
	Subject        string
	Body           string
	NotifyURL      string
	ReturnURL      string
	ClientIP       string
	ChannelExtra   string
	ExpiredTimeSec int
}

type UnifiedOrderResp struct {
	PayOrderID  string
	OrderState  int
	PayDataType string
	PayData     string
}

type QueryResp struct {
	PayOrderID string
	State      int
	AmountFen  int64
}

func (c *Client) UnifiedOrder(req UnifiedOrderReq) (*UnifiedOrderResp, error) {
	if req.Currency == "" {
		req.Currency = "cny"
	}
	params := c.mchParams()
	params["mchOrderNo"] = req.MchOrderNo
	params["wayCode"] = req.WayCode
	params["amount"] = strconv.FormatInt(req.AmountFen, 10)
	params["currency"] = req.Currency
	params["subject"] = req.Subject
	params["body"] = req.Body
	params["notifyUrl"] = req.NotifyURL
	params["returnUrl"] = req.ReturnURL
	params["clientIp"] = req.ClientIP
	if req.ChannelExtra != "" {
		params["channelExtra"] = req.ChannelExtra
	}
	if req.ExpiredTimeSec > 0 {
		params["expiredTime"] = strconv.Itoa(req.ExpiredTimeSec)
	}
	raw, err := c.signedPost("/api/pay/unifiedOrder", params)
	if err != nil {
		return nil, err
	}
	data, err := c.unwrapSigned(raw)
	if err != nil {
		return nil, err
	}
	return &UnifiedOrderResp{
		PayOrderID:  str(data["payOrderId"]),
		OrderState:  asInt(data["orderState"]),
		PayDataType: str(data["payDataType"]),
		PayData:     str(data["payData"]),
	}, nil
}

func (c *Client) QueryByMchOrderNo(mchOrderNo string) (*QueryResp, error) {
	params := c.mchParams()
	params["mchOrderNo"] = mchOrderNo
	raw, err := c.signedPost("/api/pay/query", params)
	if err != nil {
		return nil, err
	}
	data, err := c.unwrapSigned(raw)
	if err != nil {
		return nil, err
	}
	return &QueryResp{
		PayOrderID: str(data["payOrderId"]),
		State:      asInt(data["state"]),
		AmountFen:  asInt64(data["amount"]),
	}, nil
}

type CloseOutcome struct {
	Kind string
	Msg  string
}

// CloseByMchOrderNo 调用商户关单。网络失败返回 error。业务拒绝放在 Kind，不当成网络失败。
func (c *Client) CloseByMchOrderNo(mchOrderNo string) (CloseOutcome, error) {
	params := c.mchParams()
	params["mchOrderNo"] = mchOrderNo
	raw, err := c.signedPost("/api/pay/close", params)
	if err != nil {
		return CloseOutcome{}, err
	}
	msg := str(raw["msg"])
	if asInt(raw["code"]) != 0 {
		return CloseOutcome{Kind: ClassifyClose(asInt(raw["code"]), msg), Msg: msg}, nil
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	if err := verifyDataSign(data, str(raw["sign"]), c.AppSecret); err != nil {
		return CloseOutcome{Kind: CloseFailed, Msg: msg}, nil
	}
	return CloseOutcome{Kind: CloseSuccess, Msg: msg}, nil
}

// ClassifyClose 只把明确的已支付文案当成已支付。其余失败都不能当作关单成功。
func ClassifyClose(code int, msg string) string {
	if code == 0 {
		return CloseSuccess
	}
	upper := strings.ToUpper(msg)
	if strings.Contains(upper, "ORDERPAID") || strings.Contains(msg, "已支付") {
		return ClosePaid
	}
	if strings.Contains(msg, "当前订单不可关闭") {
		return CloseRejected
	}
	return CloseFailed
}

func (c *Client) mchParams() map[string]string {
	return map[string]string{
		"mchNo":    c.MchNo,
		"appId":    c.AppID,
		"reqTime":  strconv.FormatInt(time.Now().UnixMilli(), 10),
		"version":  "1.0",
		"signType": "MD5",
	}
}

func (c *Client) signedPost(path string, params map[string]string) (map[string]any, error) {
	params["sign"] = Sign(params, c.AppSecret)
	return c.postJSON(path, params)
}

func (c *Client) postJSON(path string, params map[string]string) (map[string]any, error) {
	body := make(map[string]any, len(params))
	for k, v := range params {
		if v == "" {
			continue
		}
		if k == "amount" || k == "expiredTime" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			body[k] = n
			continue
		}
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := httpClient.Post(c.BaseURL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("请求 Jeepay 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Jeepay 响应不是 JSON: %s", truncate(string(raw), 200))
	}
	return out, nil
}

func (c *Client) unwrapSigned(raw map[string]any) (map[string]any, error) {
	data, err := unwrap(raw)
	if err != nil {
		return nil, err
	}
	if err := verifyDataSign(data, str(raw["sign"]), c.AppSecret); err != nil {
		return nil, err
	}
	return data, nil
}

func unwrap(raw map[string]any) (map[string]any, error) {
	if asInt(raw["code"]) != 0 {
		msg := str(raw["msg"])
		if msg == "" {
			msg = "Jeepay 业务失败"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return map[string]any{}, nil
	}
	return data, nil
}

func verifyDataSign(data map[string]any, sign, secret string) error {
	if sign == "" {
		return fmt.Errorf("Jeepay 响应缺少签名")
	}
	params := make(map[string]string, len(data))
	for k, v := range data {
		s := str(v)
		if s == "" {
			continue
		}
		params[k] = s
	}
	if !strings.EqualFold(sign, Sign(params, secret)) {
		return fmt.Errorf("Jeepay 响应验签失败")
	}
	return nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func asInt(v any) int {
	return int(asInt64(v))
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
