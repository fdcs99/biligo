package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
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
			if createCalls.Add(1) == 1 {
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
	if len(createSKUs) < 2 || createSKUs[0] != 3002 || createSKUs[1] != 3003 {
		t.Fatalf("create skus = %#v, want [3002 3003]", createSKUs)
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

func createRunnableTask(t *testing.T) (*store.Store, model.Task) {
	t.Helper()
	return createRunnableTaskAt(t, time.Now().Add(-time.Second))
}

func createRunnableTaskAt(t *testing.T, saleStart time.Time) (*store.Store, model.Task) {
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
		SaleStart:          saleStart.Format("2006-01-02 15:04:05"),
		SaleStatus:         "未开始",
		OrderType:          1,
		BuyerInfo:          []model.TicketBuyer{{ID: 7, Name: "张三", PersonalID: "110101199001010000", Tel: "13800000000"}},
		Buyer:              "张三",
		Tel:                "13800000000",
		DeliverInfo:        &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000", FullAddress: "上海市测试路 1 号"},
		EndAt:              saleStart.Add(10 * time.Second).Format(time.RFC3339),
		PollIntervalMillis: 50,
	})
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateTask: %v", err)
	}
	return taskStore, task
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
							"sale_start": time.Now().Add(-time.Second).Format("2006-01-02 15:04:05"),
							"clickable":  standardAvailable,
						},
						{
							"id":         3002,
							"desc":       "VIP",
							"price":      88000,
							"sale_start": time.Now().Add(-time.Second).Format("2006-01-02 15:04:05"),
							"clickable":  vipAvailable,
						},
						{
							"id":         3003,
							"desc":       "SVIP",
							"price":      128000,
							"sale_start": time.Now().Add(-time.Second).Format("2006-01-02 15:04:05"),
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

func writeRunnerJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("Encode: %v", err)
	}
}
