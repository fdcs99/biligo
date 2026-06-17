package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fdcs99/biligo/internal/model"
)

func TestHTTPSenderPushPlus(t *testing.T) {
	var payload map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPSender(server.Client())
	sender.PushPlusURL = server.URL
	err := sender.Send(context.Background(), model.Notification{
		Provider: model.NotificationProviderPushPlus,
		Config:   map[string]string{"token": "push-token"},
	}, "测试标题", "测试内容")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if payload["token"] != "push-token" || payload["title"] != "测试标题" || payload["content"] != "测试内容" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestHTTPSenderBarkToken(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPSender(server.Client())
	sender.BarkBaseURL = server.URL
	err := sender.Send(context.Background(), model.Notification{
		Provider: model.NotificationProviderBark,
		Config:   map[string]string{"token": "bark-token"},
	}, "抢票成功", "请支付")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(gotPath, "/bark-token/") || !strings.Contains(gotPath, "%E6%8A%A2%E7%A5%A8%E6%88%90%E5%8A%9F") {
		t.Fatalf("unexpected bark path: %s", gotPath)
	}
}

func TestHTTPSenderBarkFullURL(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPSender(server.Client())
	err := sender.Send(context.Background(), model.Notification{
		Provider: model.NotificationProviderBark,
		Config:   map[string]string{"token": server.URL + "/custom-key"},
	}, "Biligo", "订单创建成功")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(gotPath, "/custom-key/Biligo/") || !strings.Contains(gotPath, "%E8%AE%A2%E5%8D%95%E5%88%9B%E5%BB%BA%E6%88%90%E5%8A%9F") {
		t.Fatalf("unexpected bark full url path: %s", gotPath)
	}
}
