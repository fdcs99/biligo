package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fdcs99/biligo/internal/model"
)

func TestCreateTaskPersistsFullPurchaseConfig(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	task, err := store.CreateTask(context.Background(), model.TaskInput{
		Name:        "测试任务",
		AccountID:   1,
		ProjectID:   1001701,
		ProjectName: "测试项目",
		ScreenID:    2001,
		SKUID:       3001,
		SessionName: "晚场",
		TicketLevel: "VIP",
		TicketPrice: 68000,
		SaleStart:   "2026-06-13 20:00:00",
		SelectedTickets: []model.TicketOption{
			{Value: "1001701:2001:3001:0", ProjectID: 1001701, ScreenID: 2001, SKUID: 3001, TicketLevel: "VIP", Price: 68000, Clickable: true},
		},
		OrderType: 1,
		BuyerInfo: []model.TicketBuyer{
			{Name: "张三", PersonalID: "110101199001010000"},
			{Name: "李四", PersonalID: "110101199001010001"},
		},
		Buyer: "张三",
		Tel:   "13800000000",
		DeliverInfo: &model.TicketAddress{
			ID:          9,
			Name:        "张三",
			Phone:       "13800000000",
			FullAddress: "上海市测试路 1 号",
		},
		PollIntervalMillis: 200,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if task.Quantity != 2 {
		t.Fatalf("Quantity = %d, want 2", task.Quantity)
	}
	if task.PayMoney != 136000 {
		t.Fatalf("PayMoney = %d, want 136000", task.PayMoney)
	}
	if len(task.BuyerInfo) != 2 {
		t.Fatalf("BuyerInfo len = %d, want 2", len(task.BuyerInfo))
	}
	if task.DeliverInfo == nil || task.DeliverInfo.ID != 9 {
		t.Fatalf("DeliverInfo = %#v, want address id 9", task.DeliverInfo)
	}
	if task.TimeSyncStrategy != model.TimeSyncStrategyBilibili {
		t.Fatalf("TimeSyncStrategy = %q, want %q", task.TimeSyncStrategy, model.TimeSyncStrategyBilibili)
	}
	if task.TaskMode != model.TaskModeRush {
		t.Fatalf("TaskMode = %q, want %q", task.TaskMode, model.TaskModeRush)
	}
	if task.DurationMode != model.DurationModeLimited {
		t.Fatalf("DurationMode = %q, want %q", task.DurationMode, model.DurationModeLimited)
	}
	if len(task.SelectedTickets) != 1 || task.SelectedTickets[0].SKUID != 3001 || !task.SelectedTickets[0].Clickable {
		t.Fatalf("SelectedTickets = %#v", task.SelectedTickets)
	}
	if task.RushDurationSeconds != model.DefaultRushDurationSeconds {
		t.Fatalf("RushDurationSeconds = %d, want %d", task.RushDurationSeconds, model.DefaultRushDurationSeconds)
	}
	if task.PollIntervalMillis != 200 {
		t.Fatalf("PollIntervalMillis = %d, want 200", task.PollIntervalMillis)
	}

	task, err = store.UpdateTask(context.Background(), task.ID, model.TaskInput{
		Name:                task.Name,
		AccountID:           task.AccountID,
		ProjectID:           task.ProjectID,
		ProjectName:         task.ProjectName,
		ScreenID:            task.ScreenID,
		SKUID:               task.SKUID,
		SessionName:         task.SessionName,
		TicketLevel:         task.TicketLevel,
		TicketDisplay:       task.TicketDisplay,
		TicketPrice:         task.TicketPrice,
		SaleStart:           task.SaleStart,
		SaleStatus:          task.SaleStatus,
		LinkID:              task.LinkID,
		IsHotProject:        task.IsHotProject,
		TaskMode:            model.TaskModeHybrid,
		DurationMode:        model.DurationModeUnlimited,
		SelectedTickets:     task.SelectedTickets,
		RushDurationSeconds: 45,
		OrderType:           task.OrderType,
		PayMoney:            task.PayMoney,
		BuyerInfo:           task.BuyerInfo,
		Buyer:               task.Buyer,
		Tel:                 task.Tel,
		DeliverInfo:         task.DeliverInfo,
		Phone:               task.Phone,
		TimeSyncStrategy:    task.TimeSyncStrategy,
		Quantity:            task.Quantity,
		StartAt:             task.StartAt,
		EndAt:               task.EndAt,
		PollIntervalMillis:  task.PollIntervalMillis,
	})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if task.TaskMode != model.TaskModeHybrid || task.RushDurationSeconds != 45 {
		t.Fatalf("updated task mode/duration = %q/%d", task.TaskMode, task.RushDurationSeconds)
	}

	task, log, err := store.SetTaskTimeSync(context.Background(), task.ID, model.TimeSyncStrategyBilibili, 88, "2026-06-14T10:00:00+08:00", "时间同步完成")
	if err != nil {
		t.Fatalf("SetTaskTimeSync: %v", err)
	}
	if log.ID == 0 {
		t.Fatal("time sync log was not created")
	}
	if task.TimeOffsetMillis != 88 || task.TimeSyncedAt == "" {
		t.Fatalf("unexpected time sync fields: %#v", task)
	}

	task, log, err = store.SetTaskPayMoney(context.Background(), task.ID, 140000, "金额已更新")
	if err != nil {
		t.Fatalf("SetTaskPayMoney: %v", err)
	}
	if log.ID == 0 {
		t.Fatal("pay money log was not created")
	}
	if task.PayMoney != 140000 {
		t.Fatalf("PayMoney = %d, want 140000", task.PayMoney)
	}
}

func TestMigrateCreatesCurrentTaskSchemaOnly(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if tableExists(t, store, "ticket_groups") {
		t.Fatal("ticket_groups table should not be created")
	}
	if !tableExists(t, store, "notifications") {
		t.Fatal("notifications table should be created")
	}
	if columnExists(t, store, "tasks", "ticket_group_id") {
		t.Fatal("tasks.ticket_group_id should not be created")
	}

	for _, column := range []string{
		"project_id",
		"project_name",
		"screen_id",
		"sku_id",
		"ticket_display",
		"ticket_price",
		"sale_start",
		"sale_status",
		"link_id",
		"is_hot_project",
		"task_mode",
		"duration_mode",
		"selected_tickets",
		"rush_duration_seconds",
		"order_type",
		"pay_money",
		"buyer_info",
		"deliver_info",
		"time_sync_strategy",
		"time_offset_ms",
		"time_synced_at",
		"poll_interval_ms",
	} {
		if !columnExists(t, store, "tasks", column) {
			t.Fatalf("tasks.%s should exist", column)
		}
	}
}

func TestNotificationStoreCRUD(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	notification, err := store.CreateNotification(context.Background(), model.NotificationInput{
		Name:     "PushPlus",
		Provider: model.NotificationProviderPushPlus,
		Config:   map[string]string{"token": "push-token"},
	})
	if err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}
	if notification.ID == 0 || notification.Enabled {
		t.Fatalf("unexpected created notification: %#v", notification)
	}
	if notification.Config["token"] != "push-token" {
		t.Fatalf("Config = %#v", notification.Config)
	}

	notification, err = store.UpdateNotification(context.Background(), notification.ID, model.NotificationInput{
		Name:     "Bark",
		Provider: model.NotificationProviderBark,
		Config:   map[string]string{"token": "https://bark.example.app/key"},
	})
	if err != nil {
		t.Fatalf("UpdateNotification: %v", err)
	}
	if notification.Provider != model.NotificationProviderBark || notification.Config["token"] != "https://bark.example.app/key" {
		t.Fatalf("unexpected updated notification: %#v", notification)
	}

	notification, err = store.SetNotificationEnabled(context.Background(), notification.ID, true)
	if err != nil {
		t.Fatalf("SetNotificationEnabled: %v", err)
	}
	if !notification.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	enabled, err := store.ListEnabledNotifications(context.Background())
	if err != nil {
		t.Fatalf("ListEnabledNotifications: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ID != notification.ID {
		t.Fatalf("enabled notifications = %#v", enabled)
	}

	notification, err = store.SetNotificationTestResult(context.Background(), notification.ID, "success", "测试推送已发送。")
	if err != nil {
		t.Fatalf("SetNotificationTestResult: %v", err)
	}
	if notification.LastTestStatus != "success" || notification.LastTestMessage == "" || notification.LastTestedAt == "" {
		t.Fatalf("unexpected test result: %#v", notification)
	}

	if err := store.DeleteNotification(context.Background(), notification.ID); err != nil {
		t.Fatalf("DeleteNotification: %v", err)
	}
	notifications, err := store.ListNotifications(context.Background())
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(notifications) != 0 {
		t.Fatalf("notifications = %#v, want empty", notifications)
	}
}

func TestPauseInterruptedTasks(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	interruptedStatuses := []string{"waiting_start", "running"}
	interruptedIDs := make([]int64, 0, len(interruptedStatuses))
	for _, status := range interruptedStatuses {
		task := createTestTask(t, store, "任务-"+status)
		updated, _, err := store.SetTaskRuntime(context.Background(), task.ID, model.TaskRuntimeUpdate{
			Status:      status,
			LastMessage: "运行中",
		}, "info")
		if err != nil {
			t.Fatalf("SetTaskRuntime %s: %v", status, err)
		}
		interruptedIDs = append(interruptedIDs, updated.ID)
	}

	waitingPayment := createTestTask(t, store, "待支付任务")
	if _, _, err := store.SetTaskRuntime(context.Background(), waitingPayment.ID, model.TaskRuntimeUpdate{
		Status:      "waiting_payment",
		LastMessage: "等待用户支付",
	}, "info"); err != nil {
		t.Fatalf("SetTaskRuntime waiting_payment: %v", err)
	}
	draft := createTestTask(t, store, "草稿任务")

	paused, err := store.PauseInterruptedTasks(context.Background())
	if err != nil {
		t.Fatalf("PauseInterruptedTasks: %v", err)
	}
	if len(paused) != len(interruptedStatuses) {
		t.Fatalf("paused len = %d, want %d", len(paused), len(interruptedStatuses))
	}
	for index, task := range paused {
		if task.ID != interruptedIDs[index] {
			t.Fatalf("paused[%d].ID = %d, want %d", index, task.ID, interruptedIDs[index])
		}
		if task.Status != "paused" {
			t.Fatalf("task %d status = %q, want paused", task.ID, task.Status)
		}
		if task.LastMessage != interruptedTaskMessage {
			t.Fatalf("task %d LastMessage = %q", task.ID, task.LastMessage)
		}
		logs, err := store.ListTaskLogs(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("ListTaskLogs %d: %v", task.ID, err)
		}
		if len(logs) == 0 || logs[0].Level != "warn" || logs[0].Message != interruptedTaskMessage {
			t.Fatalf("unexpected latest log for task %d: %#v", task.ID, logs)
		}
	}

	waitingPayment, err = store.GetTask(context.Background(), waitingPayment.ID)
	if err != nil {
		t.Fatalf("GetTask waiting_payment: %v", err)
	}
	if waitingPayment.Status != "waiting_payment" {
		t.Fatalf("waitingPayment status = %q, want waiting_payment", waitingPayment.Status)
	}
	draft, err = store.GetTask(context.Background(), draft.ID)
	if err != nil {
		t.Fatalf("GetTask draft: %v", err)
	}
	if draft.Status != "draft" {
		t.Fatalf("draft status = %q, want draft", draft.Status)
	}
}

func TestSetTaskRuntimeFillsLastCheckedAt(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	task := createTestTask(t, store, "最近检测任务")
	updated, _, err := store.SetTaskRuntime(context.Background(), task.ID, model.TaskRuntimeUpdate{
		Status:      "running",
		LastMessage: "运行中",
	}, "info")
	if err != nil {
		t.Fatalf("SetTaskRuntime: %v", err)
	}
	if updated.LastCheckedAt == "" {
		t.Fatal("LastCheckedAt should be filled")
	}
	if _, err := time.Parse(time.RFC3339Nano, updated.LastCheckedAt); err != nil {
		t.Fatalf("LastCheckedAt should be RFC3339Nano: %v", err)
	}
}

func createTestTask(t *testing.T, store *Store, name string) model.Task {
	t.Helper()

	task, err := store.CreateTask(context.Background(), model.TaskInput{
		Name:               name,
		AccountID:          1,
		ProjectID:          1001701,
		ProjectName:        "测试项目",
		ScreenID:           2001,
		SKUID:              3001,
		SessionName:        "晚场",
		TicketLevel:        "VIP",
		TicketDisplay:      "晚场 - VIP",
		TicketPrice:        68000,
		SaleStart:          "2026-06-13 20:00:00",
		OrderType:          1,
		BuyerInfo:          []model.TicketBuyer{{Name: "张三", PersonalID: "110101199001010000"}},
		Buyer:              "张三",
		Tel:                "13800000000",
		DeliverInfo:        &model.TicketAddress{ID: 9, Name: "张三", Phone: "13800000000"},
		PollIntervalMillis: 1000,
	})
	if err != nil {
		t.Fatalf("CreateTask %s: %v", name, err)
	}
	return task
}

func tableExists(t *testing.T, store *Store, name string) bool {
	t.Helper()

	var count int
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = ?
	`, name).Scan(&count); err != nil {
		t.Fatalf("query table %s: %v", name, err)
	}
	return count > 0
}

func columnExists(t *testing.T, store *Store, table string, column string) bool {
	t.Helper()

	rows, err := store.db.QueryContext(context.Background(), fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatalf("query columns for %s: %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan columns for %s: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns for %s: %v", table, err)
	}
	return false
}
