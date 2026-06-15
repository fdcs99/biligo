package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fdcs99/biligo/internal/applog"
	"github.com/fdcs99/biligo/internal/model"
	"github.com/fdcs99/biligo/internal/panelauth"
	"github.com/fdcs99/biligo/internal/store"
	"github.com/gin-gonic/gin"
)

func TestDeleteTaskWritesLog(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	task, err := taskStore.CreateTask(context.Background(), model.TaskInput{
		Name:               "待删除任务",
		AccountID:          1,
		ProjectID:          1001701,
		ScreenID:           2001,
		SKUID:              3001,
		TicketDisplay:      "晚场 - VIP",
		SaleStart:          "2026-06-13 20:00:00",
		BuyerInfo:          []model.TicketBuyer{{Name: "张三", PersonalID: "110101199001010000"}},
		Buyer:              "张三",
		Tel:                "13800000000",
		DeliverInfo:        &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000"},
		PollIntervalMillis: 1000,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	router := newTestRouter(taskStore)
	token := loginTestPanel(t, router, "panel-secret")
	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}

	if _, err := taskStore.GetTask(context.Background(), task.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask err = %v, want sql.ErrNoRows", err)
	}
	logs, err := taskStore.ListTaskLogs(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListTaskLogs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("delete log was not created")
	}
	if logs[0].TaskID != task.ID || logs[0].Level != "warn" || logs[0].Message != "任务已删除：待删除任务。" {
		t.Fatalf("unexpected delete log: %#v", logs[0])
	}
}

func TestPanelAuthProtectsAPIs(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	router := newTestRouter(taskStore)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("tasks without token status = %d, want 401", resp.Code)
	}

	badBody := bytes.NewBufferString(`{"password":"wrong"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/panel-auth/login", badBody)
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", resp.Code)
	}

	token := loginTestPanel(t, router, "panel-secret")
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("tasks with token status = %d, body = %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/panel-auth/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestPanelAuthProtectsEventsAndAllowsQueryToken(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	router := newTestRouter(taskStore)

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("events without token status = %d, want 401", resp.Code)
	}

	token := loginTestPanel(t, router, "panel-secret")
	ctx, cancel := context.WithCancel(context.Background())
	req = httptest.NewRequest(http.MethodGet, "/api/events?token="+token, nil).WithContext(ctx)
	resp = httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(resp, req)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("events request did not stop after context cancel")
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("events with query token status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestPanelAuthLogsLoginLogoutAndTaskLogs(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	var out bytes.Buffer
	router := NewRouter(
		taskStore,
		panelauth.NewManager("panel-secret", 24*time.Hour),
		applog.NewWithWriter([]string{"info", "warn", "error"}, &out),
	)
	token := loginTestPanel(t, router, "panel-secret")
	if !strings.Contains(out.String(), "[INFO] 面板登录成功") {
		t.Fatalf("login log missing: %q", out.String())
	}

	task, err := taskStore.CreateTask(context.Background(), model.TaskInput{
		Name:               "待删除任务",
		AccountID:          1,
		ProjectID:          1001701,
		ScreenID:           2001,
		SKUID:              3001,
		TicketDisplay:      "晚场 - VIP",
		SaleStart:          "2026-06-13 20:00:00",
		BuyerInfo:          []model.TicketBuyer{{Name: "张三", PersonalID: "110101199001010000"}},
		Buyer:              "张三",
		Tel:                "13800000000",
		DeliverInfo:        &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000"},
		PollIntervalMillis: 1000,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(out.String(), "[WARN] 任务日志实时同步：任务 1：任务已删除："+task.Name+"。") {
		t.Fatalf("task log sync missing: %q", out.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/panel-auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(out.String(), "[INFO] 面板退出登录成功") {
		t.Fatalf("logout log missing: %q", out.String())
	}
}

func newTestRouter(taskStore *store.Store) *gin.Engine {
	return NewRouter(taskStore, panelauth.NewManager("panel-secret", 24*time.Hour), applog.NewWithWriter([]string{"none"}, nil))
}

func loginTestPanel(t *testing.T, router *gin.Engine, password string) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/panel-auth/login", bytes.NewBufferString(`{"password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var auth model.PanelAuthResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &auth); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if auth.Token == "" || auth.ExpiresAt == "" {
		t.Fatalf("unexpected login response: %#v", auth)
	}
	return auth.Token
}
