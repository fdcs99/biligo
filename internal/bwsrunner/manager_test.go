package bwsrunner

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fdcs99/biligo/internal/bws"
	"github.com/fdcs99/biligo/internal/events"
	"github.com/fdcs99/biligo/internal/model"
	"github.com/fdcs99/biligo/internal/store"
	"github.com/fdcs99/biligo/internal/timesync"
)

type fakeTimeSync struct{}

func (fakeTimeSync) Sync(context.Context) (timesync.Result, error) {
	return timesync.Result{OffsetMillis: 25, AverageRTTMillis: 3, SyncedAt: time.Now()}, nil
}

func TestDispatchWaitsUntilReserveBeginTimeAndSucceeds(t *testing.T) {
	var reserveCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reserve/do" {
			t.Fatalf("path = %s, want /reserve/do", r.URL.Path)
		}
		reserveCalls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.Form.Get("csrf") != "csrf" || r.Form.Get("ticket_no") != "TICKET-10" || r.Form.Get("inter_reserve_id") != "12345" {
			t.Fatalf("form = %#v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success"})
	}))
	defer server.Close()

	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	account, err := taskStore.CreateAccount(context.Background(), model.AccountInput{Name: "账号", Cookie: "SESSDATA=x; bili_jct=csrf"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	reserveBeginTime := time.Now().Add(2 * time.Second).Unix()
	task, err := taskStore.CreateBWSTask(context.Background(), model.BWSTaskInput{
		Name:               "BWS",
		AccountID:          account.ID,
		ReserveType:        model.BWSReserveTypeActivity,
		Year:               "202601",
		ReserveDate:        "20260710",
		TicketNo:           "TICKET-10",
		ReserveID:          12345,
		Title:              "测试活动",
		ReserveBeginTime:   reserveBeginTime,
		TimeSyncStrategy:   model.TimeSyncStrategyBilibili,
		PollIntervalMillis: 1,
	})
	if err != nil {
		t.Fatalf("CreateBWSTask: %v", err)
	}

	manager := NewManagerWithTimeSync(taskStore, bws.NewClient(server.Client()).WithBaseURL(server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if reserveCalls.Load() != 0 {
		t.Fatalf("reserve endpoint called before reserveBeginTime: %d", reserveCalls.Load())
	}
	waiting, err := taskStore.GetBWSTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetBWSTask waiting: %v", err)
	}
	if waiting.Status != "waiting_start" {
		t.Fatalf("task status before reserveBeginTime = %q, want waiting_start", waiting.Status)
	}
	if !strings.Contains(waiting.LastMessage, "距离预约开始还有") {
		t.Fatalf("waiting message = %q, want countdown like rush task", waiting.LastMessage)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		latest, err := taskStore.GetBWSTask(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("GetBWSTask: %v", err)
		}
		if latest.Status == "success" {
			if reserveCalls.Load() != 1 {
				t.Fatalf("reserve endpoint calls = %d, want 1", reserveCalls.Load())
			}
			logs, err := taskStore.ListBWSTaskLogs(context.Background(), task.ID)
			if err != nil {
				t.Fatalf("ListBWSTaskLogs: %v", err)
			}
			if len(logs) == 0 {
				t.Fatal("expected logs")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	latest, _ := taskStore.GetBWSTask(context.Background(), task.ID)
	t.Fatalf("task status = %q message = %q, want success", latest.Status, latest.LastMessage)
}

func TestDispatchImmediateSkipsReserveBeginTimeWait(t *testing.T) {
	var reserveCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reserveCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success"})
	}))
	defer server.Close()

	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()

	account, err := taskStore.CreateAccount(context.Background(), model.AccountInput{Name: "账号", Cookie: "SESSDATA=x; bili_jct=csrf"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	task, err := taskStore.CreateBWSTask(context.Background(), model.BWSTaskInput{
		Name:               "BWS立即",
		AccountID:          account.ID,
		ReserveType:        model.BWSReserveTypeActivity,
		Year:               "202601",
		ReserveDate:        "20260710",
		TicketNo:           "TICKET-10",
		ReserveID:          12345,
		Title:              "测试活动",
		ReserveBeginTime:   time.Now().Add(time.Hour).Unix(),
		Mode:               model.BWSTaskModeImmediate,
		TimeSyncStrategy:   model.TimeSyncStrategyLocal,
		PollIntervalMillis: 1,
	})
	if err != nil {
		t.Fatalf("CreateBWSTask: %v", err)
	}

	manager := NewManagerWithTimeSync(taskStore, bws.NewClient(server.Client()).WithBaseURL(server.URL), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	latest := waitForBWSTaskStatus(t, taskStore, task.ID, "success")
	if reserveCalls.Load() != 1 {
		t.Fatalf("reserve calls = %d, want 1", reserveCalls.Load())
	}
	if !strings.Contains(latest.LastMessage, "预约成功") {
		t.Fatalf("last message = %q", latest.LastMessage)
	}
}

func TestRoundRobinProxySwitchesOn412(t *testing.T) {
	var firstProxyCalls atomic.Int64
	firstProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstProxyCalls.Add(1)
		http.Error(w, "risk", http.StatusPreconditionFailed)
	}))
	defer firstProxy.Close()

	var secondProxyCalls atomic.Int64
	secondProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondProxyCalls.Add(1)
		if r.URL.Path != "/reserve/do" {
			t.Fatalf("path = %s, want /reserve/do", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success"})
	}))
	defer secondProxy.Close()

	taskStore, task := createProxyBWSTask(t, model.ProxyModeRoundRobin)
	defer taskStore.Close()
	if _, err := taskStore.CreateProxyNode(context.Background(), task.ProxyGroupID, proxyNodeInputFromServer(t, "first", firstProxy.URL)); err != nil {
		t.Fatalf("CreateProxyNode first: %v", err)
	}
	if _, err := taskStore.CreateProxyNode(context.Background(), task.ProxyGroupID, proxyNodeInputFromServer(t, "second", secondProxy.URL)); err != nil {
		t.Fatalf("CreateProxyNode second: %v", err)
	}

	manager := NewManagerWithTimeSync(taskStore, bws.NewClient(nil).WithBaseURL("http://bws.test"), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	latest := waitForBWSTaskStatus(t, taskStore, task.ID, "success")
	if !strings.Contains(latest.LastMessage, "预约成功") {
		t.Fatalf("last message = %q", latest.LastMessage)
	}
	if firstProxyCalls.Load() == 0 || secondProxyCalls.Load() == 0 {
		t.Fatalf("proxy calls first=%d second=%d, want both proxies used", firstProxyCalls.Load(), secondProxyCalls.Load())
	}
}

func TestConcurrentProxyStopsAfterOneSuccess(t *testing.T) {
	firstHit := make(chan struct{})
	var firstClosed atomic.Bool
	firstProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if firstClosed.CompareAndSwap(false, true) {
			close(firstHit)
		}
		http.Error(w, "risk", http.StatusPreconditionFailed)
	}))
	defer firstProxy.Close()

	var successCalls atomic.Int64
	successProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-firstHit:
		case <-time.After(200 * time.Millisecond):
		}
		successCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success"})
	}))
	defer successProxy.Close()

	taskStore, task := createProxyBWSTask(t, model.ProxyModeConcurrent)
	defer taskStore.Close()
	if _, err := taskStore.CreateProxyNode(context.Background(), task.ProxyGroupID, proxyNodeInputFromServer(t, "first", firstProxy.URL)); err != nil {
		t.Fatalf("CreateProxyNode first: %v", err)
	}
	if _, err := taskStore.CreateProxyNode(context.Background(), task.ProxyGroupID, proxyNodeInputFromServer(t, "success", successProxy.URL)); err != nil {
		t.Fatalf("CreateProxyNode success: %v", err)
	}

	manager := NewManagerWithTimeSync(taskStore, bws.NewClient(nil).WithBaseURL("http://bws.test"), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitForBWSTaskStatus(t, taskStore, task.ID, "success")
	if successCalls.Load() == 0 {
		t.Fatal("success proxy was not called")
	}
}

func TestAPIProxyGroupPullsBeforeBWSReserve(t *testing.T) {
	var proxyCalls atomic.Int64
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success"})
	}))
	defer proxyServer.Close()

	originalPull := pullKuaidailiDPS
	var pullCalls atomic.Int64
	pullKuaidailiDPS = func(context.Context, model.ProxyGroup) ([]model.ProxyNodeInput, error) {
		pullCalls.Add(1)
		return []model.ProxyNodeInput{proxyNodeInputFromServer(t, "api", proxyServer.URL)}, nil
	}
	defer func() {
		pullKuaidailiDPS = originalPull
	}()

	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer taskStore.Close()
	account, err := taskStore.CreateAccount(context.Background(), model.AccountInput{Name: "账号", Cookie: "SESSDATA=x; bili_jct=csrf"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	group, err := taskStore.CreateProxyGroup(context.Background(), model.ProxyGroupInput{
		Name:        "API代理",
		Type:        model.ProxyGroupTypeAPI,
		APIProvider: model.ProxyProviderKuaidailiDPS,
		APIConfig: map[string]string{
			"secretId":          "id",
			"secretKey":         "key",
			"pullBeforeMinutes": "1",
			"pullTimes":         "2",
		},
	})
	if err != nil {
		t.Fatalf("CreateProxyGroup: %v", err)
	}
	task, err := taskStore.CreateBWSTask(context.Background(), model.BWSTaskInput{
		Name:               "BWS",
		AccountID:          account.ID,
		ProxyGroupID:       group.ID,
		ProxyMode:          model.ProxyModeRoundRobin,
		ReserveType:        model.BWSReserveTypeActivity,
		Year:               "202601",
		ReserveDate:        "20260710",
		TicketNo:           "TICKET-10",
		ReserveID:          12345,
		Title:              "测试活动",
		ReserveBeginTime:   time.Now().Add(-time.Second).Unix(),
		TimeSyncStrategy:   model.TimeSyncStrategyLocal,
		PollIntervalMillis: 1,
	})
	if err != nil {
		t.Fatalf("CreateBWSTask: %v", err)
	}

	manager := NewManagerWithTimeSync(taskStore, bws.NewClient(nil).WithBaseURL("http://bws.test"), events.NewHub(), fakeTimeSync{})
	if _, err := manager.Dispatch(context.Background(), task.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitForBWSTaskStatus(t, taskStore, task.ID, "success")
	if pullCalls.Load() != 2 {
		t.Fatalf("pull calls = %d, want 2", pullCalls.Load())
	}
	if proxyCalls.Load() == 0 {
		t.Fatal("pulled proxy was not used")
	}
}

func TestFormatRemainingMatchesRushCountdownStyle(t *testing.T) {
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

func createProxyBWSTask(t *testing.T, proxyMode string) (*store.Store, model.BWSTask) {
	t.Helper()
	taskStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	account, err := taskStore.CreateAccount(context.Background(), model.AccountInput{Name: "账号", Cookie: "SESSDATA=x; bili_jct=csrf"})
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateAccount: %v", err)
	}
	group, err := taskStore.CreateProxyGroup(context.Background(), model.ProxyGroupInput{
		Name: "BWS代理",
		Type: model.ProxyGroupTypeStatic,
	})
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateProxyGroup: %v", err)
	}
	task, err := taskStore.CreateBWSTask(context.Background(), model.BWSTaskInput{
		Name:               "BWS",
		AccountID:          account.ID,
		ProxyGroupID:       group.ID,
		ProxyMode:          proxyMode,
		ReserveType:        model.BWSReserveTypeActivity,
		Year:               "202601",
		ReserveDate:        "20260710",
		TicketNo:           "TICKET-10",
		ReserveID:          12345,
		Title:              "测试活动",
		ReserveBeginTime:   time.Now().Add(-time.Second).Unix(),
		TimeSyncStrategy:   model.TimeSyncStrategyLocal,
		PollIntervalMillis: 1,
	})
	if err != nil {
		taskStore.Close()
		t.Fatalf("CreateBWSTask: %v", err)
	}
	return taskStore, task
}

func proxyNodeInputFromServer(t *testing.T, name string, rawURL string) model.ProxyNodeInput {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("Parse proxy url: %v", err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi port: %v", err)
	}
	return model.ProxyNodeInput{
		Name:     name,
		Protocol: model.ProxyProtocolHTTP,
		Host:     host,
		Port:     port,
	}
}

func waitForBWSTaskStatus(t *testing.T, taskStore *store.Store, taskID int64, status string) model.BWSTask {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		task, err := taskStore.GetBWSTask(context.Background(), taskID)
		if err != nil {
			t.Fatalf("GetBWSTask: %v", err)
		}
		if task.Status == status {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, err := taskStore.GetBWSTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetBWSTask deadline: %v", err)
	}
	t.Fatalf("task status = %q message = %q, want %q", task.Status, task.LastMessage, status)
	return task
}
