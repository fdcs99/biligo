package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
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

func TestAccountImportExportAPIs(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	router := newTestRouter(taskStore)
	token := loginTestPanel(t, router, "panel-secret")

	body := bytes.NewBufferString(`{"accounts":[{"name":"主账号","cookie":"SESSDATA=main; bili_jct=token","note":"常用"},{"name":"备用账号","cookie":"SESSDATA=backup","note":"备用"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/import", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("import status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var imported model.AccountImportResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &imported); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if imported.Imported != 2 || len(imported.Accounts) != 2 || imported.Accounts[0].Name != "主账号" {
		t.Fatalf("unexpected import response: %#v", imported)
	}

	body = bytes.NewBufferString(fmt.Sprintf(`{"accountIds":[%d,%d]}`, imported.Accounts[0].ID, imported.Accounts[1].ID))
	req = httptest.NewRequest(http.MethodPost, "/api/accounts/export", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("batch export status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var batchExported model.AccountBatchExportResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &batchExported); err != nil {
		t.Fatalf("decode batch export response: %v", err)
	}
	if batchExported.Version != 1 || len(batchExported.Accounts) != 2 || batchExported.Accounts[1].Name != "备用账号" {
		t.Fatalf("unexpected batch export response: %#v", batchExported)
	}

	body = bytes.NewBufferString(`{"account":{"name":"单账号","cookie":"SESSDATA=single"}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/accounts/import", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "accounts 数组") {
		t.Fatalf("single import status = %d, body = %s", resp.Code, resp.Body.String())
	}

	body = bytes.NewBufferString(`{"name":"单账号","cookie":"SESSDATA=single"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/accounts/import", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "accounts 数组") {
		t.Fatalf("root single import status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestTaskAPIsRejectConcurrentProxyWithoutGroup(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	router := newTestRouter(taskStore)
	token := loginTestPanel(t, router, "panel-secret")
	body := bytes.NewBufferString(`{"name":"并发代理任务","accountId":1,"proxyMode":"concurrent","projectId":1001701,"screenId":2001,"skuId":3001,"ticketDisplay":"晚场 - VIP","saleStart":"2026-06-13 20:00:00","buyerInfo":[{"name":"张三","personalId":"110101199001010000"}],"buyer":"张三","tel":"13800000000","deliverInfo":{"id":9,"name":"张三","phone":"13800000000"},"pollIntervalMillis":1000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "并发代理需要选择代理组") {
		t.Fatalf("create concurrent proxy task status = %d, body = %s", resp.Code, resp.Body.String())
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

func TestNotificationAPIs(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	sender := &fakeNotificationSender{}
	router := NewRouter(
		taskStore,
		panelauth.NewManager("panel-secret", 24*time.Hour),
		applog.NewWithWriter([]string{"none"}, nil),
		WithNotificationSender(sender),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("notifications without token status = %d, want 401", resp.Code)
	}

	token := loginTestPanel(t, router, "panel-secret")
	body := bytes.NewBufferString(`{"name":"PushPlus","provider":"pushplus","config":{"token":"push-token"}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/notifications", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create notification status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var notification model.Notification
	if err := json.Unmarshal(resp.Body.Bytes(), &notification); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if notification.ID == 0 || notification.Provider != model.NotificationProviderPushPlus || notification.Config["token"] != "push-token" {
		t.Fatalf("unexpected notification: %#v", notification)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/notifications/1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("test notification status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if sender.calls != 1 || sender.lastTitle != "Biligo 通知测试" {
		t.Fatalf("unexpected sender state: %#v", sender)
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &notification); err != nil {
		t.Fatalf("decode tested notification: %v", err)
	}
	if notification.LastTestStatus != "success" || notification.LastTestedAt == "" {
		t.Fatalf("unexpected test status: %#v", notification)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/notifications/1/enable", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("enable notification status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &notification); err != nil {
		t.Fatalf("decode enabled notification: %v", err)
	}
	if !notification.Enabled {
		t.Fatalf("Enabled = false, want true")
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/notifications/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("delete notification status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestProxyAPIsAndBusyGuard(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	router := newTestRouter(taskStore)
	token := loginTestPanel(t, router, "panel-secret")

	body := bytes.NewBufferString(`{"name":"代理组","type":"static","apiProvider":"","apiConfig":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/proxy-groups", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create proxy group status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var group model.ProxyGroup
	if err := json.Unmarshal(resp.Body.Bytes(), &group); err != nil {
		t.Fatalf("decode proxy group: %v", err)
	}
	if group.ID == 0 || group.Type != model.ProxyGroupTypeStatic {
		t.Fatalf("unexpected proxy group: %#v", group)
	}

	body = bytes.NewBufferString(`{"name":"节点1","protocol":"http","host":"127.0.0.1","port":8080,"username":"u","password":"p"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/proxy-groups/1/nodes", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create proxy node status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var node model.ProxyNode
	if err := json.Unmarshal(resp.Body.Bytes(), &node); err != nil {
		t.Fatalf("decode proxy node: %v", err)
	}
	if node.ID == 0 || node.Username != "u" {
		t.Fatalf("unexpected proxy node: %#v", node)
	}

	body = bytes.NewBufferString(`{"name":"API代理组","type":"api","apiProvider":"kuaidaili_dps","apiConfig":{"secretId":"sid","secretKey":"skey"}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/proxy-groups", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create api proxy group status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var apiGroup model.ProxyGroup
	if err := json.Unmarshal(resp.Body.Bytes(), &apiGroup); err != nil {
		t.Fatalf("decode api proxy group: %v", err)
	}
	body = bytes.NewBufferString(`{"name":"节点2","protocol":"http","host":"127.0.0.1","port":8081}`)
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/proxy-groups/%d/nodes", apiGroup.ID), body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "不支持手动添加") {
		t.Fatalf("create api proxy node status = %d, body = %s", resp.Code, resp.Body.String())
	}

	task, err := taskStore.CreateTask(context.Background(), model.TaskInput{
		Name:               "代理占用任务",
		AccountID:          1,
		ProxyGroupID:       group.ID,
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
	if _, _, err := taskStore.SetTaskRuntime(context.Background(), task.ID, model.TaskRuntimeUpdate{
		Status:      "running",
		LastMessage: "运行中",
	}, "info"); err != nil {
		t.Fatalf("SetTaskRuntime: %v", err)
	}

	body = bytes.NewBufferString(`{"name":"代理组2","type":"static","apiProvider":"","apiConfig":{}}`)
	req = httptest.NewRequest(http.MethodPut, "/api/proxy-groups/1", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "无法编辑") {
		t.Fatalf("busy update status = %d, body = %s", resp.Code, resp.Body.String())
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

type fakeNotificationSender struct {
	calls     int
	lastTitle string
}

func (f *fakeNotificationSender) Send(ctx context.Context, notification model.Notification, title string, content string) error {
	f.calls++
	f.lastTitle = title
	return nil
}

func TestWebUIRoutesDoNotInterceptAPI(t *testing.T) {
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	router := NewRouter(
		taskStore,
		panelauth.NewManager("panel-secret", 24*time.Hour),
		applog.NewWithWriter([]string{"none"}, nil),
		WithWebFS(fstest.MapFS{
			"index.html":    {Data: []byte("<html><body>Biligo App</body></html>")},
			"assets/app.js": {Data: []byte("console.log('biligo')")},
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "Biligo App") {
		t.Fatalf("index status = %d, body = %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "console.log") {
		t.Fatalf("asset status = %d, body = %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/task/detail/1", nil)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "Biligo App") {
		t.Fatalf("spa fallback status = %d, body = %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/not-found", nil)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound || strings.Contains(resp.Body.String(), "Biligo App") {
		t.Fatalf("api fallback status = %d, body = %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api", nil)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound || strings.Contains(resp.Body.String(), "Biligo App") {
		t.Fatalf("bare api fallback status = %d, body = %s", resp.Code, resp.Body.String())
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
