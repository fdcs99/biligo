package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fdcs99/biligo/internal/biliticket"
	"github.com/fdcs99/biligo/internal/events"
	"github.com/fdcs99/biligo/internal/model"
	"github.com/fdcs99/biligo/internal/notify"
	"github.com/fdcs99/biligo/internal/store"
	"github.com/fdcs99/biligo/internal/tickettime"
	"github.com/fdcs99/biligo/internal/timesync"
)

type fakeTimeSync struct {
	result timesync.Result
	err    error
}

func (f fakeTimeSync) Sync(context.Context) (timesync.Result, error) {
	if f.err != nil {
		return timesync.Result{}, f.err
	}
	if f.result.SyncedAt.IsZero() {
		f.result.SyncedAt = time.Now()
	}
	if f.result.TotalSampleCount == 0 {
		f.result.TotalSampleCount = 5
	}
	if f.result.AveragedSampleCount == 0 {
		f.result.AveragedSampleCount = 3
	}
	return f.result, nil
}

func TestValidateTaskRequiresPurchaseConfig(t *testing.T) {
	task := model.Task{
		AccountID:   1,
		ProjectID:   1001701,
		ScreenID:    2001,
		SKUID:       3001,
		SaleStart:   "2026-06-13 20:00:00",
		BuyerInfo:   []model.TicketBuyer{{Name: "张三", PersonalID: "110101199001010000"}},
		Buyer:       "张三",
		Tel:         "13800000000",
		DeliverInfo: &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000"},
	}
	if err := validateTask(task); err != nil {
		t.Fatalf("validateTask returned error: %v", err)
	}

	task.BuyerInfo = nil
	if err := validateTask(task); err == nil {
		t.Fatal("validateTask returned nil for missing buyer info")
	}
}

func TestValidateConcurrentProxyRequiresProxyGroup(t *testing.T) {
	task := model.Task{
		AccountID:   1,
		ProjectID:   1001701,
		ScreenID:    2001,
		SKUID:       3001,
		SaleStart:   "2026-06-13 20:00:00",
		ProxyMode:   model.ProxyModeConcurrent,
		BuyerInfo:   []model.TicketBuyer{{Name: "张三", PersonalID: "110101199001010000"}},
		Buyer:       "张三",
		Tel:         "13800000000",
		DeliverInfo: &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000"},
	}
	if err := validateTask(task); err == nil {
		t.Fatal("validateTask returned nil for concurrent proxy without proxy group")
	}
	task.ProxyGroupID = 10
	if err := validateTask(task); err != nil {
		t.Fatalf("validateTask returned error: %v", err)
	}
}

func TestValidateRestockTaskRequiresSelectedTickets(t *testing.T) {
	task := model.Task{
		AccountID:       1,
		ProjectID:       1001701,
		TaskMode:        model.TaskModeRestock,
		DurationMode:    model.DurationModeUnlimited,
		BuyerInfo:       []model.TicketBuyer{{Name: "张三", PersonalID: "110101199001010000"}},
		Buyer:           "张三",
		Tel:             "13800000000",
		DeliverInfo:     &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000"},
		SelectedTickets: nil,
	}
	if err := validateTask(task); err == nil {
		t.Fatal("validateTask returned nil for missing selected tickets")
	}
	task.SelectedTickets = []model.TicketOption{{ProjectID: 1001701, ScreenID: 2001, SKUID: 3001}}
	if err := validateTask(task); err != nil {
		t.Fatalf("validateTask returned error: %v", err)
	}
}

func TestValidateHybridTaskRequirements(t *testing.T) {
	task := model.Task{
		AccountID:           1,
		ProjectID:           1001701,
		ScreenID:            2001,
		SKUID:               3001,
		SaleStart:           "2026-06-13 20:00:00",
		TaskMode:            model.TaskModeHybrid,
		DurationMode:        model.DurationModeUnlimited,
		SelectedTickets:     []model.TicketOption{{ProjectID: 1001701, ScreenID: 2001, SKUID: 3001}},
		RushDurationSeconds: 600,
		BuyerInfo:           []model.TicketBuyer{{Name: "张三", PersonalID: "110101199001010000"}},
		Buyer:               "张三",
		Tel:                 "13800000000",
		DeliverInfo:         &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000"},
	}
	if err := validateTask(task); err != nil {
		t.Fatalf("validateTask returned error: %v", err)
	}

	task.SelectedTickets = nil
	if err := validateTask(task); err == nil {
		t.Fatal("validateTask returned nil for hybrid task without restock tickets")
	}
	task.SelectedTickets = []model.TicketOption{{ProjectID: 1001701, ScreenID: 2001, SKUID: 3001}}
	task.RushDurationSeconds = 0
	if err := validateTask(task); err == nil {
		t.Fatal("validateTask returned nil for hybrid task without rush duration")
	}
	task.RushDurationSeconds = 600
	task.DurationMode = model.DurationModeLimited
	task.EndAt = ""
	if err := validateTask(task); err == nil {
		t.Fatal("validateTask returned nil for hybrid limited task without end time")
	}
}

func TestParseTaskTimeTreatsPlainTimesAsChinaTime(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() {
		time.Local = originalLocal
	})

	parsed, err := parseTaskTime("2026-06-13 20:00:00")
	if err != nil {
		t.Fatalf("parseTaskTime: %v", err)
	}
	want, err := time.Parse(time.RFC3339, "2026-06-13T20:00:00+08:00")
	if err != nil {
		t.Fatalf("parse wanted time: %v", err)
	}
	if !parsed.Equal(want) {
		t.Fatalf("parseTaskTime = %v, want %v", parsed, want)
	}

	parsedMinute, err := parseTaskTime("2026-06-13T20:00")
	if err != nil {
		t.Fatalf("parseTaskTime minute format: %v", err)
	}
	if !parsedMinute.Equal(want) {
		t.Fatalf("parseTaskTime minute format = %v, want %v", parsedMinute, want)
	}
}

func TestParseTaskTimeHonorsRFC3339Zone(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() {
		time.Local = originalLocal
	})

	parsed, err := parseTaskTime("2026-06-13T20:00:00Z")
	if err != nil {
		t.Fatalf("parseTaskTime: %v", err)
	}
	want, err := time.Parse(time.RFC3339, "2026-06-13T20:00:00Z")
	if err != nil {
		t.Fatalf("parse wanted time: %v", err)
	}
	if !parsed.Equal(want) {
		t.Fatalf("parseTaskTime = %v, want %v", parsed, want)
	}
}

func TestWaitUntilSaleStartCanBeCanceled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if manager.waitUntilSaleStart(ctx, 0, time.Now().Add(time.Hour), 0, nil, nil) {
		t.Fatal("waitUntilSaleStart returned true after cancellation")
	}
}

func TestPauseTaskUsesStopMessage(t *testing.T) {
	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, nil, events.NewHub(), fakeTimeSync{})
	updated, err := manager.Pause(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if updated.Status != "paused" {
		t.Fatalf("Status = %q, want paused", updated.Status)
	}
	if updated.LastMessage != "任务已停止。" {
		t.Fatalf("LastMessage = %q, want 任务已停止。", updated.LastMessage)
	}
}

func TestFormatRemaining(t *testing.T) {
	if got := formatRemaining(3661 * time.Second); got != "1小时1分1秒" {
		t.Fatalf("formatRemaining = %q, want 1小时1分1秒", got)
	}
	if got := formatRemaining(61 * time.Second); got != "1分1秒" {
		t.Fatalf("formatRemaining = %q, want 1分1秒", got)
	}
	if got := formatRemaining(time.Second); got != "1秒" {
		t.Fatalf("formatRemaining = %q, want 1秒", got)
	}
}

func TestProxyRuntimePullBeforeUsesAPIConfig(t *testing.T) {
	runtime := &taskProxyRuntime{
		group: model.ProxyGroup{
			Type: model.ProxyGroupTypeAPI,
			APIConfig: map[string]string{
				"pullBeforeMinutes": "2",
			},
		},
	}
	if !runtime.shouldPull(90 * time.Second) {
		t.Fatal("shouldPull = false, want true inside configured window")
	}
	if runtime.shouldPull(3 * time.Minute) {
		t.Fatal("shouldPull = true, want false outside configured window")
	}

	runtime.group.APIConfig["pullBeforeMinutes"] = "bad"
	if runtime.pullBefore() != defaultProxyAPIPullBefore {
		t.Fatalf("pullBefore = %v, want default %v", runtime.pullBefore(), defaultProxyAPIPullBefore)
	}
}

func TestRunnerStartsOrderFlowWithoutTicketStatusCheck(t *testing.T) {
	var statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			statusCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayload(false))
		case "/api/ticket/linkgoods/list":
			statusCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{
		result: timesync.Result{OffsetMillis: 123, AverageRTTMillis: 8},
	})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-1" {
		t.Fatalf("OrderID = %q, want ORDER-1", updated.OrderID)
	}
	if updated.PaymentURL != "https://pay.example.test/order/ORDER-1" {
		t.Fatalf("PaymentURL = %q", updated.PaymentURL)
	}
	if updated.PaymentQRImageDataURL == "" {
		t.Fatal("PaymentQRImageDataURL is empty")
	}
	if updated.TimeSyncStrategy != model.TimeSyncStrategyBilibili {
		t.Fatalf("TimeSyncStrategy = %q, want %q", updated.TimeSyncStrategy, model.TimeSyncStrategyBilibili)
	}
	if updated.TimeOffsetMillis != 123 {
		t.Fatalf("TimeOffsetMillis = %d, want 123", updated.TimeOffsetMillis)
	}
	if updated.TimeSyncedAt == "" {
		t.Fatal("TimeSyncedAt is empty")
	}
	if statusCalls.Load() != 0 {
		t.Fatalf("ticket status calls = %d, want 0", statusCalls.Load())
	}
}

func TestRunnerWarmupsShowHomeBeforeSaleStart(t *testing.T) {
	var headCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			if r.Method != http.MethodHead {
				t.Fatalf("warmup method = %s, want HEAD", r.Method)
			}
			headCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "/mall-search-items/items_detail/info":
			writeRunnerJSON(t, w, ticketDetailPayload(false))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTaskAt(t, time.Now().Add(2*time.Second))
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-1" {
		t.Fatalf("OrderID = %q, want ORDER-1", updated.OrderID)
	}
	if headCalls.Load() != saleStartWarmupRequestCount {
		t.Fatalf("HEAD calls = %d, want %d", headCalls.Load(), saleStartWarmupRequestCount)
	}
}

func TestRunnerRefreshesHotProjectBeforeSaleStart(t *testing.T) {
	var infoCalls atomic.Int32
	var prepareToken atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusNoContent)
		case "/mall-search-items/items_detail/info":
			infoCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayloadWithHotProject(true, true))
		case "/api/ticket/project/getV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{}})
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			payload := decodeRunnerJSONBody(t, r)
			prepareToken.Store(stringValueFromBody(payload["token"]))
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token", "ptoken": "hot-ptoken"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-HOT", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-HOT"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTaskAt(t, time.Now().Add(2*time.Second))
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if !updated.IsHotProject {
		t.Fatal("IsHotProject = false, want true after pre-sale refresh")
	}
	if infoCalls.Load() != 1 {
		t.Fatalf("project info calls = %d, want 1", infoCalls.Load())
	}
	if token, _ := prepareToken.Load().(string); token == "" {
		t.Fatal("prepare token is empty, want hot project ctoken")
	}
}

func TestRunnerLogsHotProjectPreSaleCheckWhenStateUnchanged(t *testing.T) {
	var infoCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusNoContent)
		case "/mall-search-items/items_detail/info":
			infoCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayloadWithHotProject(true, false))
		case "/api/ticket/project/getV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{}})
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-HOT-UNCHANGED", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-HOT-UNCHANGED"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTaskAt(t, time.Now().Add(2*time.Second))
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.IsHotProject {
		t.Fatal("IsHotProject = true, want false after unchanged refresh")
	}
	if infoCalls.Load() != 1 {
		t.Fatalf("project info calls = %d, want 1", infoCalls.Load())
	}
	logs, err := taskStore.ListTaskLogs(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTaskLogs: %v", err)
	}
	foundStartLog := false
	foundUnchangedLog := false
	for _, log := range logs {
		if strings.Contains(log.Message, "开票前 5 分钟开始校验 hot_project 状态") {
			foundStartLog = true
		}
		if strings.Contains(log.Message, "开票前 hot_project 状态校验完成，远端状态与本地一致：false") {
			foundUnchangedLog = true
		}
	}
	if !foundStartLog || !foundUnchangedLog {
		t.Fatalf("hot project check logs missing start=%v unchanged=%v logs=%#v", foundStartLog, foundUnchangedLog, logs)
	}
}

func TestRunnerKeepsLocalHotProjectAfterPreSaleRefreshFailures(t *testing.T) {
	var infoCalls atomic.Int32
	var prepareToken atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusNoContent)
		case "/mall-search-items/items_detail/info":
			infoCalls.Add(1)
			http.Error(w, "temporary failure", http.StatusBadGateway)
		case "/api/ticket/project/getV2":
			http.Error(w, "temporary failure", http.StatusBadGateway)
		case "/api/ticket/order/prepare":
			payload := decodeRunnerJSONBody(t, r)
			prepareToken.Store(stringValueFromBody(payload["token"]))
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-LOCAL", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-LOCAL"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTaskAt(t, time.Now().Add(2*time.Second))
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.IsHotProject {
		t.Fatal("IsHotProject = true, want false after refresh failures")
	}
	if infoCalls.Load() != hotProjectCheckAttempts {
		t.Fatalf("project info calls = %d, want %d", infoCalls.Load(), hotProjectCheckAttempts)
	}
	if token, _ := prepareToken.Load().(string); token != "" {
		t.Fatalf("prepare token = %q, want empty local non-hot state", token)
	}
}

func TestRunnerPullsAPIProxyBeforeOrderWhenSaleAlreadyStarted(t *testing.T) {
	var pullCalls atomic.Int32
	var prepareCalls atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if pullCalls.Load() == 0 {
			t.Fatalf("order request %s arrived before API proxy pull", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/ticket/order/prepare":
			prepareCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-API-PROXY", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-API-PROXY"}})
		default:
			t.Fatalf("unexpected proxy path: %s", r.URL.Path)
		}
	}))
	defer proxyServer.Close()

	parsedProxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	proxyPort, err := strconv.Atoi(parsedProxyURL.Port())
	if err != nil {
		t.Fatalf("parse proxy port: %v", err)
	}
	originalPull := pullKuaidailiDPS
	pullKuaidailiDPS = func(context.Context, model.ProxyGroup) ([]model.ProxyNodeInput, error) {
		pullCalls.Add(1)
		return []model.ProxyNodeInput{{
			Name:     "API 拉取节点",
			Protocol: model.ProxyProtocolHTTP,
			Host:     parsedProxyURL.Hostname(),
			Port:     proxyPort,
		}, {
			Name:     "API 备用节点",
			Protocol: model.ProxyProtocolHTTP,
			Host:     "198.51.100.9",
			Port:     18888,
		}}, nil
	}
	t.Cleanup(func() {
		pullKuaidailiDPS = originalPull
	})

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()
	group, err := taskStore.CreateProxyGroup(context.Background(), model.ProxyGroupInput{
		Name:        "API 代理组",
		Type:        model.ProxyGroupTypeAPI,
		APIProvider: model.ProxyProviderKuaidailiDPS,
		APIConfig: map[string]string{
			"secretId":          "sid",
			"secretKey":         "skey",
			"pullBeforeMinutes": "5",
			"proxyProtocol":     "http",
		},
	})
	if err != nil {
		t.Fatalf("CreateProxyGroup: %v", err)
	}
	task = updateTaskProxyGroup(t, taskStore, task, group.ID)

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(nil, "http://show.bilibili.test"), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-API-PROXY" {
		t.Fatalf("OrderID = %q, want ORDER-API-PROXY", updated.OrderID)
	}
	if pullCalls.Load() != 1 {
		t.Fatalf("pull calls = %d, want 1", pullCalls.Load())
	}
	if prepareCalls.Load() == 0 {
		t.Fatal("order flow did not use pulled proxy node")
	}
	logs, err := taskStore.ListTaskLogs(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTaskLogs: %v", err)
	}
	foundPullLog := false
	for _, log := range logs {
		if strings.Contains(log.Message, "代理 API 拉取完成，已准备 2 个代理节点") &&
			strings.Contains(log.Message, fmt.Sprintf("%s:%d", parsedProxyURL.Hostname(), proxyPort)) &&
			strings.Contains(log.Message, "198.51.100.9:18888") {
			foundPullLog = true
			break
		}
	}
	if !foundPullLog {
		t.Fatalf("api pull success log missing: %#v", logs)
	}
}

func TestRunnerSendsNotificationOnWaitingPayment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()
	if _, err := taskStore.CreateNotification(context.Background(), model.NotificationInput{
		Name:     "PushPlus",
		Provider: model.NotificationProviderPushPlus,
		Config:   map[string]string{"token": "push-token"},
	}); err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}
	if _, err := taskStore.SetNotificationEnabled(context.Background(), 1, true); err != nil {
		t.Fatalf("SetNotificationEnabled: %v", err)
	}

	sender := &fakeRunnerNotificationSender{}
	hub := events.NewHub()
	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), hub, fakeTimeSync{})
	manager.SetNotifier(notify.NewService(taskStore, sender, hub))
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-1" {
		t.Fatalf("OrderID = %q, want ORDER-1", updated.OrderID)
	}
	waitForNotificationCalls(t, sender, 1)
	logs, err := taskStore.ListTaskLogs(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTaskLogs: %v", err)
	}
	found := false
	for _, log := range logs {
		if log.Message == "通知推送成功：PushPlus。" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("notification success log missing: %#v", logs)
	}
}

func TestRunnerRetriesPrepareOrderErrors(t *testing.T) {
	var prepareCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			if prepareCalls.Add(1) == 1 {
				writeRunnerJSON(t, w, map[string]any{"code": -101, "msg": "请先登录"})
				return
			}
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-1" {
		t.Fatalf("OrderID = %q, want ORDER-1", updated.OrderID)
	}
	if prepareCalls.Load() < 2 {
		t.Fatalf("prepare calls = %d, want at least 2", prepareCalls.Load())
	}
}

func TestRunnerRetriesDefaultBBRWarning(t *testing.T) {
	var prepareCalls atomic.Int32
	var createCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			prepareCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			if createCalls.Add(1) < createOrderAttemptsPerPrepare {
				writeRunnerJSON(t, w, map[string]any{"code": 0, "message": "defaultBBR"})
				return
			}
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-1" {
		t.Fatalf("OrderID = %q, want ORDER-1", updated.OrderID)
	}
	if prepareCalls.Load() != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls.Load())
	}
	if createCalls.Load() != createOrderAttemptsPerPrepare {
		t.Fatalf("create calls = %d, want %d", createCalls.Load(), createOrderAttemptsPerPrepare)
	}
}

func TestRunnerWaitsBeforeRetryingCreate412WithoutProxy(t *testing.T) {
	originalWait := noProxyCreate412RetryWait
	noProxyCreate412RetryWait = 80 * time.Millisecond
	t.Cleanup(func() {
		noProxyCreate412RetryWait = originalWait
	})

	var mu sync.Mutex
	createAttempts := make([]time.Time, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			mu.Lock()
			createAttempts = append(createAttempts, time.Now())
			attempt := len(createAttempts)
			mu.Unlock()
			if attempt == 1 {
				writeRunnerJSON(t, w, map[string]any{"code": 412, "message": "risk"})
				return
			}
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-412", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-412"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTaskWithPollInterval(t, 1)
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-412" {
		t.Fatalf("OrderID = %q, want ORDER-412", updated.OrderID)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(createAttempts) < 2 {
		t.Fatalf("create attempts = %d, want at least 2", len(createAttempts))
	}
	if elapsed := createAttempts[1].Sub(createAttempts[0]); elapsed < 60*time.Millisecond {
		t.Fatalf("create retry elapsed = %v, want no-proxy 412 wait", elapsed)
	}
}

func TestRunnerSwitchesProxyOnCreateV2RiskCodes(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{name: "code 412", code: 412},
		{name: "code 3", code: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var firstCreateCalls atomic.Int32
			var secondCreateCalls atomic.Int32
			firstProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/ticket/order/prepare":
					writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
				case "/api/ticket/order/createV2":
					firstCreateCalls.Add(1)
					writeRunnerJSON(t, w, map[string]any{"code": tt.code, "message": "risk"})
				default:
					t.Fatalf("unexpected first proxy path: %s", r.URL.Path)
				}
			}))
			defer firstProxy.Close()
			secondProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/ticket/order/prepare":
					writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
				case "/api/ticket/order/createV2":
					secondCreateCalls.Add(1)
					writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 68000}})
				case "/api/ticket/order/getPayParam":
					writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
				default:
					t.Fatalf("unexpected second proxy path: %s", r.URL.Path)
				}
			}))
			defer secondProxy.Close()

			taskStore, task := createRunnableTask(t)
			defer taskStore.Close()
			groupID := createProxyGroupForRunner(t, taskStore, firstProxy.URL, secondProxy.URL)
			task = updateTaskProxyGroup(t, taskStore, task, groupID)

			manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(nil, "http://show.bilibili.test"), events.NewHub(), fakeTimeSync{})
			if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}

			updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
			if updated.OrderID != "ORDER-1" {
				t.Fatalf("OrderID = %q, want ORDER-1", updated.OrderID)
			}
			if firstCreateCalls.Load() != 1 {
				t.Fatalf("first proxy create calls = %d, want 1", firstCreateCalls.Load())
			}
			if secondCreateCalls.Load() == 0 {
				t.Fatal("second proxy was not used after risk code")
			}
		})
	}
}

func TestRunnerConcurrentProxyStopsAfterFirstSuccess(t *testing.T) {
	var firstCreateCalls atomic.Int32
	var secondCreateCalls atomic.Int32
	firstProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			firstCreateCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 412, "message": "risk"})
		default:
			t.Fatalf("unexpected first proxy path: %s", r.URL.Path)
		}
	}))
	defer firstProxy.Close()
	secondProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			secondCreateCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-CONCURRENT", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-CONCURRENT"}})
		default:
			t.Fatalf("unexpected second proxy path: %s", r.URL.Path)
		}
	}))
	defer secondProxy.Close()

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()
	groupID := createProxyGroupForRunner(t, taskStore, firstProxy.URL, secondProxy.URL)
	task = updateTaskProxyGroupWithMode(t, taskStore, task, groupID, model.ProxyModeConcurrent)

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(nil, "http://show.bilibili.test"), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-CONCURRENT" {
		t.Fatalf("OrderID = %q, want ORDER-CONCURRENT", updated.OrderID)
	}
	if firstCreateCalls.Load() == 0 {
		t.Fatal("first proxy worker did not start")
	}
	if secondCreateCalls.Load() == 0 {
		t.Fatal("second proxy worker did not create order")
	}
}

func TestRunnerConcurrentProxyDuplicateOrderCancelsWorkers(t *testing.T) {
	var duplicateCalls atomic.Int32
	firstProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			duplicateCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 100079, "message": "duplicate"})
		default:
			t.Fatalf("unexpected first proxy path: %s", r.URL.Path)
		}
	}))
	defer firstProxy.Close()
	secondProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 412, "message": "risk"})
		default:
			t.Fatalf("unexpected second proxy path: %s", r.URL.Path)
		}
	}))
	defer secondProxy.Close()

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()
	groupID := createProxyGroupForRunner(t, taskStore, firstProxy.URL, secondProxy.URL)
	task = updateTaskProxyGroupWithMode(t, taskStore, task, groupID, model.ProxyModeConcurrent)

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(nil, "http://show.bilibili.test"), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "duplicate_order")
	if !strings.Contains(updated.LastMessage, "重复订单") {
		t.Fatalf("LastMessage = %q, want duplicate order message", updated.LastMessage)
	}
	if duplicateCalls.Load() == 0 {
		t.Fatal("duplicate proxy worker did not create order")
	}
}

func TestRunnerUpdatesPayMoneyForCreateV2PriceChange(t *testing.T) {
	var prepareCalls atomic.Int32
	var createCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			prepareCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			if createCalls.Add(1) == 1 {
				writeRunnerJSON(t, w, map[string]any{
					"code": 100034,
					"data": map[string]any{"pay_money": 69000},
				})
				return
			}
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 69000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-1" {
		t.Fatalf("OrderID = %q, want ORDER-1", updated.OrderID)
	}
	if updated.PayMoney != 69000 {
		t.Fatalf("PayMoney = %d, want 69000", updated.PayMoney)
	}
	if prepareCalls.Load() != 2 {
		t.Fatalf("prepare calls = %d, want 2", prepareCalls.Load())
	}
	if createCalls.Load() != 2 {
		t.Fatalf("create calls = %d, want 2", createCalls.Load())
	}
}

func TestRunnerRetriesPayParamErrorsWithoutRecreatingOrder(t *testing.T) {
	var createCalls atomic.Int32
	var payCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			createCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			if payCalls.Add(1) == 1 {
				writeRunnerJSON(t, w, map[string]any{"code": 503, "message": "service unavailable"})
				return
			}
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRunnableTask(t)
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.PaymentURL != "https://pay.example.test/order/ORDER-1" {
		t.Fatalf("PaymentURL = %q", updated.PaymentURL)
	}
	if createCalls.Load() != 1 {
		t.Fatalf("create calls = %d, want 1", createCalls.Load())
	}
	if payCalls.Load() < 2 {
		t.Fatalf("pay calls = %d, want at least 2", payCalls.Load())
	}
}

func TestRestockModeChecksTicketStatusBeforeOrderFlow(t *testing.T) {
	var statusCalls atomic.Int32
	var prepareCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			statusCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			prepareCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRestockTask(t, model.DurationModeUnlimited, "")
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{
		err: errors.New("restock mode should not sync time"),
	})
	dispatched, err := manager.Dispatch(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if dispatched.Status != "running" {
		t.Fatalf("dispatched status = %q, want running", dispatched.Status)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-1" {
		t.Fatalf("OrderID = %q, want ORDER-1", updated.OrderID)
	}
	if statusCalls.Load() == 0 {
		t.Fatal("ticket status was not checked")
	}
	if prepareCalls.Load() == 0 {
		t.Fatal("order flow was not started")
	}
	if updated.TimeSyncedAt != "" || updated.TimeOffsetMillis != 0 {
		t.Fatalf("restock mode should not sync time: %#v", updated)
	}
}

func TestRestockModeUsesFirstClickableSelectedTicketFromLatestProjectOrder(t *testing.T) {
	var preparedSKU atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeRunnerJSON(t, w, multiTicketDetailPayload(false, true, true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			payload := decodeRunnerJSONBody(t, r)
			preparedSKU.Store(int64FromBody(payload["sku_id"]))
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-1", "pay_money": 176000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRestockTaskWithTickets(t, model.DurationModeUnlimited, "", []model.TicketOption{
		{Value: "1001701:2001:3003:0", Display: "晚场 - SVIP", ProjectID: 1001701, ScreenID: 2001, SKUID: 3003, ScreenName: "晚场", TicketLevel: "SVIP", Price: 128000},
		{Value: "1001701:2001:3002:0", Display: "晚场 - VIP", ProjectID: 1001701, ScreenID: 2001, SKUID: 3002, ScreenName: "晚场", TicketLevel: "VIP", Price: 88000},
	})
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if preparedSKU.Load() != 3002 {
		t.Fatalf("prepared sku = %d, want 3002", preparedSKU.Load())
	}
	if updated.SKUID != 3002 || updated.TicketPrice != 88000 || updated.PayMoney != 88000 {
		t.Fatalf("matched ticket not persisted: %#v", updated)
	}
}

func TestRestockModeReturnsToTicketDetectionAfterOrderCreateFailure(t *testing.T) {
	var infoCalls atomic.Int32
	var createCalls atomic.Int32
	var mu sync.Mutex
	createSKUs := make([]int64, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			if infoCalls.Add(1) == 1 {
				writeRunnerJSON(t, w, multiTicketDetailPayload(false, true, false))
				return
			}
			writeRunnerJSON(t, w, multiTicketDetailPayload(false, false, true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			payload := decodeRunnerJSONBody(t, r)
			mu.Lock()
			createSKUs = append(createSKUs, int64FromBody(payload["sku_id"]))
			mu.Unlock()
			if createCalls.Add(1) <= createOrderAttemptsPerPrepare {
				writeRunnerJSON(t, w, map[string]any{"code": 100009, "message": "stock not enough"})
				return
			}
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-2", "pay_money": 256000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-2"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRestockTaskWithTickets(t, model.DurationModeUnlimited, "", []model.TicketOption{
		{Value: "1001701:2001:3002:0", Display: "晚场 - VIP", ProjectID: 1001701, ScreenID: 2001, SKUID: 3002, ScreenName: "晚场", TicketLevel: "VIP", Price: 88000},
		{Value: "1001701:2001:3003:0", Display: "晚场 - SVIP", ProjectID: 1001701, ScreenID: 2001, SKUID: 3003, ScreenName: "晚场", TicketLevel: "SVIP", Price: 128000},
	})
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-2" {
		t.Fatalf("OrderID = %q, want ORDER-2", updated.OrderID)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(createSKUs) < createOrderAttemptsPerPrepare+1 {
		t.Fatalf("create skus = %#v, want at least %d calls", createSKUs, createOrderAttemptsPerPrepare+1)
	}
	for index := 0; index < createOrderAttemptsPerPrepare; index++ {
		if createSKUs[index] != 3002 {
			t.Fatalf("create skus = %#v, want first %d calls use 3002", createSKUs, createOrderAttemptsPerPrepare)
		}
	}
	if createSKUs[createOrderAttemptsPerPrepare] != 3003 {
		t.Fatalf("create skus = %#v, want call %d use 3003", createSKUs, createOrderAttemptsPerPrepare+1)
	}
}

func TestRestockModeRepreparesAfterCreateV2PriceChange(t *testing.T) {
	var infoCalls atomic.Int32
	var prepareCalls atomic.Int32
	var createCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			infoCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			prepareCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			if createCalls.Add(1) == 1 {
				writeRunnerJSON(t, w, map[string]any{
					"code": 100034,
					"data": map[string]any{"pay_money": 69000},
				})
				return
			}
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-RESTOCK-1", "pay_money": 69000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-RESTOCK-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRestockTask(t, model.DurationModeUnlimited, "")
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-RESTOCK-1" {
		t.Fatalf("OrderID = %q, want ORDER-RESTOCK-1", updated.OrderID)
	}
	if updated.PayMoney != 69000 {
		t.Fatalf("PayMoney = %d, want 69000", updated.PayMoney)
	}
	if infoCalls.Load() != 1 {
		t.Fatalf("ticket info calls = %d, want 1", infoCalls.Load())
	}
	if prepareCalls.Load() != 2 {
		t.Fatalf("prepare calls = %d, want 2", prepareCalls.Load())
	}
	if createCalls.Load() != 2 {
		t.Fatalf("create calls = %d, want 2", createCalls.Load())
	}
}

func TestRestockModeDoesNotCreateOrderWhenTicketUnavailable(t *testing.T) {
	var statusCalls atomic.Int32
	var prepareCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			statusCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayload(false))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			prepareCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRestockTask(t, model.DurationModeUnlimited, "")
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	updated := waitForTaskCondition(t, taskStore, task.ID, func(task model.Task) bool {
		return statusCalls.Load() >= 2 && task.Status == "running" && task.LastCheckedAt != ""
	})
	if updated.Status != "running" {
		t.Fatalf("Status = %q, want running", updated.Status)
	}
	if prepareCalls.Load() != 0 {
		t.Fatalf("prepare calls = %d, want 0", prepareCalls.Load())
	}
	manager.Cancel(task.ID)
}

func TestRestockModeLogsRepeatedStatusChecks(t *testing.T) {
	var statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			statusCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayload(false))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRestockTask(t, model.DurationModeUnlimited, "")
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer manager.Cancel(task.ID)

	expectedMessage := "已检测 1 个已选票种，暂不可购买，继续检测。"
	first := waitForTaskCondition(t, taskStore, task.ID, func(task model.Task) bool {
		return task.LastMessage == expectedMessage && task.LastCheckedAt != ""
	})
	firstCheckedAt := first.LastCheckedAt

	updated := waitForTaskCondition(t, taskStore, task.ID, func(task model.Task) bool {
		return statusCalls.Load() >= 3 &&
			task.LastMessage == expectedMessage &&
			task.LastCheckedAt != "" &&
			task.LastCheckedAt != firstCheckedAt
	})
	if updated.LastMessage != expectedMessage {
		t.Fatalf("LastMessage = %q, want %q", updated.LastMessage, expectedMessage)
	}

	logs, err := taskStore.ListTaskLogs(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTaskLogs: %v", err)
	}
	statusLogCount := 0
	for _, log := range logs {
		if log.Message == expectedMessage {
			statusLogCount++
		}
	}
	if statusLogCount < 2 {
		t.Fatalf("status log count = %d, want at least 2", statusLogCount)
	}
}

func TestRestockLimitedModeStopsAtEndTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeRunnerJSON(t, w, ticketDetailPayload(false))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createRestockTask(t, model.DurationModeLimited, time.Now().Add(120*time.Millisecond).Format(time.RFC3339))
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	updated := waitForTaskStatus(t, taskStore, task.ID, "failed")
	if updated.LastMessage != "已超过任务结束时间，停止检测。" {
		t.Fatalf("LastMessage = %q", updated.LastMessage)
	}
}

func TestRestockUnlimitedModeAllowsEmptyEndTime(t *testing.T) {
	taskStore, task := createRestockTask(t, model.DurationModeUnlimited, "")
	defer taskStore.Close()

	if err := validateTask(task); err != nil {
		t.Fatalf("validateTask returned error: %v", err)
	}
}

func TestHybridModeRushSuccessDoesNotEnterRestock(t *testing.T) {
	var statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			statusCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-HYBRID-RUSH", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-HYBRID-RUSH"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createHybridTask(t, 600, model.DurationModeUnlimited, "")
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-HYBRID-RUSH" {
		t.Fatalf("OrderID = %q, want ORDER-HYBRID-RUSH", updated.OrderID)
	}
	if statusCalls.Load() != 0 {
		t.Fatalf("restock status calls = %d, want 0", statusCalls.Load())
	}
}

func TestHybridModeSwitchesToRestockAfterRushWindow(t *testing.T) {
	var infoCalls atomic.Int32
	var prepareCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			infoCalls.Add(1)
			writeRunnerJSON(t, w, ticketDetailPayload(true))
		case "/api/ticket/linkgoods/list":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/api/ticket/order/prepare":
			prepareCalls.Add(1)
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		case "/api/ticket/order/createV2":
			if infoCalls.Load() == 0 {
				writeRunnerJSON(t, w, map[string]any{"code": 100009, "message": "stock not enough"})
				return
			}
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "ORDER-HYBRID-RESTOCK", "pay_money": 68000}})
		case "/api/ticket/order/getPayParam":
			writeRunnerJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"code_url": "https://pay.example.test/order/ORDER-HYBRID-RESTOCK"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskStore, task := createHybridTaskAt(t, time.Now().Add(-10*time.Second), 1, model.DurationModeUnlimited, "")
	defer taskStore.Close()

	manager := NewManagerWithTimeSync(taskStore, biliticket.NewClientWithBaseURL(server.Client(), server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	updated := waitForTaskStatus(t, taskStore, task.ID, "waiting_payment")
	if updated.OrderID != "ORDER-HYBRID-RESTOCK" {
		t.Fatalf("OrderID = %q, want ORDER-HYBRID-RESTOCK", updated.OrderID)
	}
	if prepareCalls.Load() < 2 {
		t.Fatalf("prepare calls = %d, want rush and restock attempts", prepareCalls.Load())
	}
	if infoCalls.Load() == 0 {
		t.Fatal("restock ticket status was not checked after rush window")
	}
}

func createRunnableTask(t *testing.T) (*store.Store, model.Task) {
	t.Helper()
	return createRunnableTaskAt(t, time.Now().Add(-time.Second))
}

func createRunnableTaskWithPollInterval(t *testing.T, pollIntervalMillis int) (*store.Store, model.Task) {
	t.Helper()
	return createRunnableTaskAtWithPollInterval(t, time.Now().Add(-time.Second), pollIntervalMillis)
}

func createRunnableTaskAt(t *testing.T, saleStart time.Time) (*store.Store, model.Task) {
	t.Helper()
	return createRunnableTaskAtWithPollInterval(t, saleStart, 50)
}

func createRunnableTaskAtWithPollInterval(t *testing.T, saleStart time.Time, pollIntervalMillis int) (*store.Store, model.Task) {
	t.Helper()

	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	account, err := taskStore.CreateAccountWithStatus(context.Background(), model.AccountInput{
		Name:   "测试账号",
		Cookie: "SESSDATA=test",
	}, "logged_in")
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateAccountWithStatus: %v", err)
	}
	task, err := taskStore.CreateTask(context.Background(), model.TaskInput{
		Name:               "测试任务",
		AccountID:          account.ID,
		ProjectID:          1001701,
		ProjectName:        "测试项目",
		ScreenID:           2001,
		SKUID:              3001,
		SessionName:        "晚场",
		TicketLevel:        "VIP",
		TicketDisplay:      "晚场 - VIP",
		TicketPrice:        68000,
		SaleStart:          formatBusinessTaskTime(saleStart),
		SaleStatus:         "未开始",
		OrderType:          1,
		BuyerInfo:          []model.TicketBuyer{{ID: 7, Name: "张三", PersonalID: "110101199001010000", Tel: "13800000000"}},
		Buyer:              "张三",
		Tel:                "13800000000",
		DeliverInfo:        &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000", FullAddress: "上海市测试路 1 号"},
		EndAt:              saleStart.Add(10 * time.Second).Format(time.RFC3339),
		PollIntervalMillis: pollIntervalMillis,
	})
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateTask: %v", err)
	}
	return taskStore, task
}

func createProxyGroupForRunner(t *testing.T, taskStore *store.Store, proxyURLs ...string) int64 {
	t.Helper()
	group, err := taskStore.CreateProxyGroup(context.Background(), model.ProxyGroupInput{
		Name: "代理组",
		Type: model.ProxyGroupTypeStatic,
	})
	if err != nil {
		t.Fatalf("CreateProxyGroup: %v", err)
	}
	for index, rawURL := range proxyURLs {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse proxy URL: %v", err)
		}
		host := parsed.Hostname()
		port, err := strconv.Atoi(parsed.Port())
		if err != nil {
			t.Fatalf("parse proxy port: %v", err)
		}
		if _, err := taskStore.CreateProxyNode(context.Background(), group.ID, model.ProxyNodeInput{
			Name:     fmt.Sprintf("代理-%d", index+1),
			Protocol: model.ProxyProtocolHTTP,
			Host:     host,
			Port:     port,
		}); err != nil {
			t.Fatalf("CreateProxyNode: %v", err)
		}
	}
	return group.ID
}

func updateTaskProxyGroup(t *testing.T, taskStore *store.Store, task model.Task, proxyGroupID int64) model.Task {
	t.Helper()
	return updateTaskProxyGroupWithMode(t, taskStore, task, proxyGroupID, model.ProxyModeRoundRobin)
}

func updateTaskProxyGroupWithMode(t *testing.T, taskStore *store.Store, task model.Task, proxyGroupID int64, proxyMode string) model.Task {
	t.Helper()
	updated, err := taskStore.UpdateTask(context.Background(), task.ID, model.TaskInput{
		Name:               task.Name,
		AccountID:          task.AccountID,
		ProxyGroupID:       proxyGroupID,
		ProxyMode:          proxyMode,
		ProjectID:          task.ProjectID,
		ProjectName:        task.ProjectName,
		ScreenID:           task.ScreenID,
		SKUID:              task.SKUID,
		SessionName:        task.SessionName,
		TicketLevel:        task.TicketLevel,
		TicketDisplay:      task.TicketDisplay,
		TicketPrice:        task.TicketPrice,
		SaleStart:          task.SaleStart,
		SaleStatus:         task.SaleStatus,
		LinkID:             task.LinkID,
		IsHotProject:       task.IsHotProject,
		TaskMode:           task.TaskMode,
		DurationMode:       task.DurationMode,
		SelectedTickets:    task.SelectedTickets,
		OrderType:          task.OrderType,
		PayMoney:           task.PayMoney,
		BuyerInfo:          task.BuyerInfo,
		Buyer:              task.Buyer,
		Tel:                task.Tel,
		DeliverInfo:        task.DeliverInfo,
		Phone:              task.Phone,
		TimeSyncStrategy:   task.TimeSyncStrategy,
		Quantity:           task.Quantity,
		StartAt:            task.StartAt,
		EndAt:              task.EndAt,
		PollIntervalMillis: task.PollIntervalMillis,
	})
	if err != nil {
		t.Fatalf("UpdateTask proxy group: %v", err)
	}
	return updated
}

func createRestockTask(t *testing.T, durationMode string, endAt string) (*store.Store, model.Task) {
	t.Helper()
	return createRestockTaskWithTickets(t, durationMode, endAt, []model.TicketOption{
		{Value: "1001701:2001:3001:0", Display: "晚场 - VIP", ProjectID: 1001701, ScreenID: 2001, SKUID: 3001, ScreenName: "晚场", TicketLevel: "VIP", Price: 68000},
	})
}

func createRestockTaskWithTickets(t *testing.T, durationMode string, endAt string, selectedTickets []model.TicketOption) (*store.Store, model.Task) {
	t.Helper()

	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	account, err := taskStore.CreateAccountWithStatus(context.Background(), model.AccountInput{
		Name:   "测试账号",
		Cookie: "SESSDATA=test",
	}, "logged_in")
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateAccountWithStatus: %v", err)
	}
	task, err := taskStore.CreateTask(context.Background(), model.TaskInput{
		Name:               "回流捡漏任务",
		AccountID:          account.ID,
		ProjectID:          1001701,
		ProjectName:        "测试项目",
		ScreenID:           2001,
		SKUID:              3001,
		SessionName:        "晚场",
		TicketLevel:        "VIP",
		TicketDisplay:      "晚场 - VIP",
		TicketPrice:        68000,
		SaleStatus:         "暂时售罄",
		TaskMode:           model.TaskModeRestock,
		DurationMode:       durationMode,
		SelectedTickets:    selectedTickets,
		OrderType:          1,
		BuyerInfo:          []model.TicketBuyer{{ID: 7, Name: "张三", PersonalID: "110101199001010000", Tel: "13800000000"}},
		Buyer:              "张三",
		Tel:                "13800000000",
		DeliverInfo:        &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000", FullAddress: "上海市测试路 1 号"},
		EndAt:              endAt,
		PollIntervalMillis: 30,
	})
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateTask: %v", err)
	}
	return taskStore, task
}

func createHybridTask(t *testing.T, rushDurationSeconds int, durationMode string, endAt string) (*store.Store, model.Task) {
	t.Helper()
	return createHybridTaskAt(t, time.Now().Add(-time.Second), rushDurationSeconds, durationMode, endAt)
}

func createHybridTaskAt(t *testing.T, saleStart time.Time, rushDurationSeconds int, durationMode string, endAt string) (*store.Store, model.Task) {
	t.Helper()

	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	account, err := taskStore.CreateAccountWithStatus(context.Background(), model.AccountInput{
		Name:   "测试账号",
		Cookie: "SESSDATA=test",
	}, "logged_in")
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateAccountWithStatus: %v", err)
	}
	task, err := taskStore.CreateTask(context.Background(), model.TaskInput{
		Name:                "组合任务",
		AccountID:           account.ID,
		ProjectID:           1001701,
		ProjectName:         "测试项目",
		ScreenID:            2001,
		SKUID:               3001,
		SessionName:         "晚场",
		TicketLevel:         "VIP",
		TicketDisplay:       "晚场 - VIP",
		TicketPrice:         68000,
		SaleStart:           formatBusinessTaskTime(saleStart),
		SaleStatus:          "未开始",
		TaskMode:            model.TaskModeHybrid,
		DurationMode:        durationMode,
		SelectedTickets:     []model.TicketOption{{Value: "1001701:2001:3001:0", Display: "晚场 - VIP", ProjectID: 1001701, ScreenID: 2001, SKUID: 3001, ScreenName: "晚场", TicketLevel: "VIP", Price: 68000}},
		RushDurationSeconds: rushDurationSeconds,
		OrderType:           1,
		BuyerInfo:           []model.TicketBuyer{{ID: 7, Name: "张三", PersonalID: "110101199001010000", Tel: "13800000000"}},
		Buyer:               "张三",
		Tel:                 "13800000000",
		DeliverInfo:         &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000", FullAddress: "上海市测试路 1 号"},
		EndAt:               endAt,
		PollIntervalMillis:  50,
	})
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateTask: %v", err)
	}
	return taskStore, task
}

func waitForTaskStatus(t *testing.T, taskStore *store.Store, taskID int64, status string) model.Task {
	t.Helper()

	return waitForTaskCondition(t, taskStore, taskID, func(task model.Task) bool {
		return task.Status == status
	})
}

func waitForTaskCondition(t *testing.T, taskStore *store.Store, taskID int64, condition func(model.Task) bool) model.Task {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := taskStore.GetTask(context.Background(), taskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if condition(task) {
			return task
		}
		time.Sleep(50 * time.Millisecond)
	}
	task, _ := taskStore.GetTask(context.Background(), taskID)
	t.Fatalf("task condition not met; status=%q message=%q", task.Status, task.LastMessage)
	return model.Task{}
}

type fakeRunnerNotificationSender struct {
	calls atomic.Int32
}

func (f *fakeRunnerNotificationSender) Send(ctx context.Context, notification model.Notification, title string, content string) error {
	f.calls.Add(1)
	return nil
}

func waitForNotificationCalls(t *testing.T, sender *fakeRunnerNotificationSender, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sender.calls.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("notification calls = %d, want %d", sender.calls.Load(), want)
}

func formatBusinessTaskTime(value time.Time) string {
	return value.In(tickettime.Location()).Format("2006-01-02 15:04:05")
}

func ticketDetailPayload(available bool) map[string]any {
	return ticketDetailPayloadWithHotProject(available, false)
}

func ticketDetailPayloadWithHotProject(available bool, hotProject bool) map[string]any {
	return map[string]any{
		"code":    0,
		"success": true,
		"data": map[string]any{
			"projectId":   1001701,
			"projectName": "测试项目",
			"hotProject":  hotProject,
			"screenList": []map[string]any{
				{
					"id":          2001,
					"name":        "晚场",
					"start_time":  time.Now().Add(-time.Hour).Unix(),
					"express_fee": 0,
					"ticket_list": []map[string]any{
						{
							"id":         3001,
							"desc":       "VIP",
							"price":      68000,
							"sale_start": formatBusinessTaskTime(time.Now().Add(-time.Second)),
							"clickable":  available,
						},
					},
				},
			},
			"skuVenueInfo": map[string]any{
				"name":           "测试场馆",
				"address_detail": "测试地址",
			},
		},
	}
}

func multiTicketDetailPayload(standardAvailable bool, vipAvailable bool, svipAvailable bool) map[string]any {
	return map[string]any{
		"code":    0,
		"success": true,
		"data": map[string]any{
			"projectId":   1001701,
			"projectName": "测试项目",
			"hotProject":  false,
			"screenList": []map[string]any{
				{
					"id":          2001,
					"name":        "晚场",
					"start_time":  time.Now().Add(-time.Hour).Unix(),
					"express_fee": 0,
					"ticket_list": []map[string]any{
						{
							"id":         3001,
							"desc":       "普通",
							"price":      68000,
							"sale_start": formatBusinessTaskTime(time.Now().Add(-time.Second)),
							"clickable":  standardAvailable,
						},
						{
							"id":         3002,
							"desc":       "VIP",
							"price":      88000,
							"sale_start": formatBusinessTaskTime(time.Now().Add(-time.Second)),
							"clickable":  vipAvailable,
						},
						{
							"id":         3003,
							"desc":       "SVIP",
							"price":      128000,
							"sale_start": formatBusinessTaskTime(time.Now().Add(-time.Second)),
							"clickable":  svipAvailable,
						},
					},
				},
			},
			"skuVenueInfo": map[string]any{
				"name":           "测试场馆",
				"address_detail": "测试地址",
			},
		},
	}
}

func decodeRunnerJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode request body: %v", err)
	}
	return payload
}

func int64FromBody(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	default:
		return 0
	}
}

func stringValueFromBody(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}

func writeRunnerJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("Encode: %v", err)
	}
}
