package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fdcs99/biligo/internal/biliticket"
	"github.com/fdcs99/biligo/internal/events"
	"github.com/fdcs99/biligo/internal/model"
	"github.com/fdcs99/biligo/internal/store"
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

func TestParseTaskTimeAcceptsSaleStartFormat(t *testing.T) {
	parsed, err := parseTaskTime("2026-06-13 20:00:00")
	if err != nil {
		t.Fatalf("parseTaskTime: %v", err)
	}
	if parsed.Year() != 2026 || parsed.Month() != 6 || parsed.Day() != 13 {
		t.Fatalf("unexpected parsed time: %v", parsed)
	}
}

func TestWaitUntilSaleStartCanBeCanceled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if manager.waitUntilSaleStart(ctx, 0, time.Now().Add(time.Hour), 0) {
		t.Fatal("waitUntilSaleStart returned true after cancellation")
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
	var createCalls atomic.Int32
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
			if createCalls.Add(1) == 1 {
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
	if createCalls.Load() < 2 {
		t.Fatalf("create calls = %d, want at least 2", createCalls.Load())
	}
}

func TestRunnerUpdatesPayMoneyForCreateV2PriceChange(t *testing.T) {
	var createCalls atomic.Int32
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
	if createCalls.Load() < 2 {
		t.Fatalf("create calls = %d, want at least 2", createCalls.Load())
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

func createRunnableTask(t *testing.T) (*store.Store, model.Task) {
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
		Name:                "测试任务",
		AccountID:           account.ID,
		ProjectID:           1001701,
		ProjectName:         "测试项目",
		ScreenID:            2001,
		SKUID:               3001,
		SessionName:         "晚场",
		TicketLevel:         "VIP",
		TicketDisplay:       "晚场 - VIP",
		TicketPrice:         68000,
		SaleStart:           time.Now().Add(-time.Second).Format("2006-01-02 15:04:05"),
		SaleStatus:          "未开始",
		OrderType:           1,
		BuyerInfo:           []model.TicketBuyer{{ID: 7, Name: "张三", PersonalID: "110101199001010000", Tel: "13800000000"}},
		Buyer:               "张三",
		Tel:                 "13800000000",
		DeliverInfo:         &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000", FullAddress: "上海市测试路 1 号"},
		EndAt:               time.Now().Add(10 * time.Second).Format(time.RFC3339),
		PollIntervalSeconds: 1,
	})
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateTask: %v", err)
	}
	return taskStore, task
}

func waitForTaskStatus(t *testing.T, taskStore *store.Store, taskID int64, status string) model.Task {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := taskStore.GetTask(context.Background(), taskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if task.Status == status {
			return task
		}
		time.Sleep(50 * time.Millisecond)
	}
	task, _ := taskStore.GetTask(context.Background(), taskID)
	t.Fatalf("task status = %q, want %q; message=%q", task.Status, status, task.LastMessage)
	return model.Task{}
}

func ticketDetailPayload(available bool) map[string]any {
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
							"desc":       "VIP",
							"price":      68000,
							"sale_start": time.Now().Add(-time.Second).Format("2006-01-02 15:04:05"),
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

func writeRunnerJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("Encode: %v", err)
	}
}
