package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fdcs99/biligo/internal/model"
)

const (
	defaultPushPlusURL = "http://www.pushplus.plus/send"
	defaultBarkBaseURL = "https://api.day.app"
)

type Sender interface {
	Send(ctx context.Context, notification model.Notification, title string, content string) error
}

type HTTPSender struct {
	client      *http.Client
	PushPlusURL string
	BarkBaseURL string
}

func NewHTTPSender(client *http.Client) *HTTPSender {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &HTTPSender{
		client:      client,
		PushPlusURL: defaultPushPlusURL,
		BarkBaseURL: defaultBarkBaseURL,
	}
}

func (s *HTTPSender) Send(ctx context.Context, notification model.Notification, title string, content string) error {
	switch model.NormalizeNotificationProvider(notification.Provider) {
	case model.NotificationProviderPushPlus:
		return s.sendPushPlus(ctx, notification, title, content)
	case model.NotificationProviderBark:
		return s.sendBark(ctx, notification, title, content)
	default:
		return fmt.Errorf("不支持的通知类型：%s", notification.Provider)
	}
}

func (s *HTTPSender) sendPushPlus(ctx context.Context, notification model.Notification, title string, content string) error {
	token := strings.TrimSpace(notification.Config["token"])
	if token == "" {
		return errors.New("PushPlus Token 不能为空")
	}
	endpoint := strings.TrimSpace(s.PushPlusURL)
	if endpoint == "" {
		endpoint = defaultPushPlusURL
	}
	payload := map[string]string{
		"token":   token,
		"title":   title,
		"content": content,
	}
	return s.postJSON(ctx, endpoint, payload)
}

func (s *HTTPSender) sendBark(ctx context.Context, notification model.Notification, title string, content string) error {
	token := strings.TrimSpace(notification.Config["token"])
	if token == "" {
		return errors.New("Bark Token 或完整推送地址不能为空")
	}
	base := strings.TrimSpace(s.BarkBaseURL)
	if base == "" {
		base = defaultBarkBaseURL
	}
	endpoint := buildBarkURL(base, token, title, content)
	payload := map[string]string{
		"icon":   "https://raw.githubusercontent.com/mikumifa/biliTickerBuy/refs/heads/main/assets/icon.ico",
		"group":  "Biligo",
		"url":    "https://mall.bilibili.com/neul/index.html?page=box_me&noTitleBar=1",
		"sound":  "telegraph",
		"level":  "critical",
		"volume": "10",
	}
	return s.postJSON(ctx, endpoint, payload)
}

func buildBarkURL(base string, token string, title string, content string) string {
	escapedTitle := url.PathEscape(title)
	escapedContent := url.PathEscape(content)
	if parsed, err := url.Parse(token); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return strings.TrimRight(token, "/") + "/" + escapedTitle + "/" + escapedContent
	}
	return strings.TrimRight(base, "/") + "/" + strings.Trim(token, "/") + "/" + escapedTitle + "/" + escapedContent
}

func (s *HTTPSender) postJSON(ctx context.Context, endpoint string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("通知接口返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
