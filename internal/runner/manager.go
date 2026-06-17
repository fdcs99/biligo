package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fdcs99/biligo/internal/biliticket"
	"github.com/fdcs99/biligo/internal/events"
	"github.com/fdcs99/biligo/internal/model"
	"github.com/fdcs99/biligo/internal/store"
	"github.com/fdcs99/biligo/internal/timesync"
)

type TimeSynchronizer interface {
	Sync(ctx context.Context) (timesync.Result, error)
}

type Manager struct {
	store    *store.Store
	ticket   *biliticket.Client
	timeSync TimeSynchronizer
	hub      *events.Hub

	mu      sync.Mutex
	running map[int64]context.CancelFunc
}

const (
	saleStartWaitTick           = time.Millisecond * 50 // 等待 50 ms
	saleStartWaitReportInterval = time.Second
	saleStartWarmupBefore       = 30 * time.Second
	saleStartWarmupRequestCount = 5
)

func NewManager(store *store.Store, ticket *biliticket.Client, hub *events.Hub) *Manager {
	return NewManagerWithTimeSync(store, ticket, hub, timesync.NewClient(nil))
}

func NewManagerWithTimeSync(store *store.Store, ticket *biliticket.Client, hub *events.Hub, timeSync TimeSynchronizer) *Manager {
	if timeSync == nil {
		timeSync = timesync.NewClient(nil)
	}
	return &Manager{
		store:    store,
		ticket:   ticket,
		timeSync: timeSync,
		hub:      hub,
		running:  map[int64]context.CancelFunc{},
	}
}

func (m *Manager) Dispatch(ctx context.Context, taskID int64) (model.Task, error) {
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return model.Task{}, err
	}
	if err := validateTask(task); err != nil {
		task, log, setErr := m.store.SetTaskRuntime(ctx, taskID, model.TaskRuntimeUpdate{
			Status:      "failed",
			LastMessage: err.Error(),
		}, "error")
		if setErr == nil {
			m.publishTaskAndLog(task, log)
		}
		return model.Task{}, err
	}
	_, cookie, err := m.store.GetAccountCookie(ctx, task.AccountID)
	if err != nil {
		return model.Task{}, err
	}
	if strings.TrimSpace(cookie) == "" {
		return model.Task{}, errors.New("账号未保存 Cookie")
	}

	if model.NormalizeTaskMode(task.TaskMode) == model.TaskModeRush {
		task, err = m.syncTaskTime(ctx, task)
		if err != nil {
			return model.Task{}, err
		}
	}

	m.mu.Lock()
	if _, ok := m.running[taskID]; ok {
		m.mu.Unlock()
		return task, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m.running[taskID] = cancel
	m.mu.Unlock()

	status := "waiting_start"
	message := "任务已下发，等待票档起售时间。"
	if model.NormalizeTaskMode(task.TaskMode) == model.TaskModeRestock {
		status = "running"
		message = "回流捡漏任务已下发，开始检测票档状态。"
	}
	task, log, err := m.store.SetTaskRuntime(ctx, taskID, model.TaskRuntimeUpdate{
		Status:      status,
		LastMessage: message,
	}, "info")
	if err != nil {
		m.remove(taskID)
		cancel()
		return model.Task{}, err
	}
	m.publishTaskAndLog(task, log)

	go m.run(runCtx, taskID, cookie)
	return task, nil
}

func (m *Manager) Pause(ctx context.Context, taskID int64) (model.Task, error) {
	m.mu.Lock()
	cancel := m.running[taskID]
	if cancel != nil {
		cancel()
		delete(m.running, taskID)
	}
	m.mu.Unlock()

	task, log, err := m.store.SetTaskRuntime(ctx, taskID, model.TaskRuntimeUpdate{
		Status:      "paused",
		LastMessage: "任务已停止。",
	}, "warn")
	if err != nil {
		return model.Task{}, err
	}
	m.publishTaskAndLog(task, log)
	return task, nil
}

func (m *Manager) Cancel(taskID int64) {
	m.mu.Lock()
	cancel := m.running[taskID]
	if cancel != nil {
		cancel()
		delete(m.running, taskID)
	}
	m.mu.Unlock()
}

func (m *Manager) run(ctx context.Context, taskID int64, cookie string) {
	defer m.remove(taskID)

	task, err := m.store.GetTask(context.Background(), taskID)
	if err != nil {
		return
	}
	if model.NormalizeTaskMode(task.TaskMode) == model.TaskModeRestock {
		m.runRestock(ctx, taskID, cookie, task)
		return
	}
	m.runRush(ctx, taskID, cookie, task)
}

func (m *Manager) runRush(ctx context.Context, taskID int64, cookie string, task model.Task) {
	saleStart, err := parseTaskTime(task.SaleStart)
	if err != nil {
		m.setRuntime(taskID, "failed", "无法解析票档起售时间："+err.Error(), "error")
		return
	}
	endAt := saleStart.Add(10 * time.Minute)
	if parsedEnd, err := parseTaskTime(task.EndAt); err == nil && !parsedEnd.IsZero() {
		endAt = parsedEnd
	}
	timeOffset := time.Duration(task.TimeOffsetMillis) * time.Millisecond

	if !m.waitUntilSaleStart(ctx, taskID, saleStart, timeOffset) {
		return
	}

	m.setRuntime(taskID, "running", "已到起售时间，开始准备订单。", "info")
	interval := time.Duration(task.PollIntervalMillis) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}

	deadlineExceeded := func() bool {
		return nowWithOffset(timeOffset).After(endAt)
	}
	m.runOrderFlow(ctx, taskID, cookie, interval, deadlineExceeded)
}

func (m *Manager) runRestock(ctx context.Context, taskID int64, cookie string, task model.Task) {
	interval := time.Duration(task.PollIntervalMillis) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}

	var deadlineExceeded func() bool
	if model.NormalizeDurationMode(task.DurationMode) == model.DurationModeLimited {
		endAt, err := parseTaskTime(task.EndAt)
		if err != nil {
			m.setRuntime(taskID, "failed", "回流捡漏有限模式需要设置合法截止时间："+err.Error(), "error")
			return
		}
		deadlineExceeded = func() bool {
			return time.Now().After(endAt)
		}
	}

	for {
		if ctx.Err() != nil {
			return
		}
		if deadlineExceeded != nil && deadlineExceeded() {
			m.setRuntime(taskID, "failed", "已超过任务结束时间，停止检测。", "warn")
			return
		}

		latestTask, err := m.store.GetTask(context.Background(), taskID)
		if err != nil {
			return
		}
		checkedAt := checkedAtText()

		_, available, err := m.ticket.CheckTicketStatus(ctx, latestTask, cookie)
		if err != nil {
			message := formatRetryMessage("票档状态检测失败：" + err.Error())
			m.setRuntimeWithCheckedAt(taskID, "running", message, "warn", checkedAt)
			if !m.wait(ctx, interval) {
				return
			}
			continue
		}

		if available {
			m.setRuntimeWithCheckedAt(taskID, "running", "检测到票档可购买，开始准备订单。", "info", checkedAt)
			m.runOrderFlow(ctx, taskID, cookie, interval, deadlineExceeded)
			return
		}
		m.setRuntimeWithCheckedAt(taskID, "running", "票档暂不可购买，继续检测", "info", checkedAt)
		if !m.wait(ctx, interval) {
			return
		}
	}
}

func (m *Manager) runOrderFlow(ctx context.Context, taskID int64, cookie string, interval time.Duration, deadlineExceeded func() bool) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		if deadlineExceeded != nil && deadlineExceeded() {
			m.setRuntime(taskID, "failed", "已超过任务结束时间，停止检测。", "warn")
			return false
		}

		latestTask, err := m.store.GetTask(context.Background(), taskID)
		if err != nil {
			return false
		}
		checkedAt := checkedAtText()

		prepared, err := m.ticket.PrepareOrder(ctx, latestTask, cookie)
		if err != nil {
			if !m.retryRunError(ctx, taskID, "订单准备失败："+err.Error(), checkedAt, interval) {
				return false
			}
			continue
		}
		result, err := m.ticket.CreateOrder(ctx, latestTask, cookie, prepared)
		if err != nil {
			if result.Code == 100079 {
				m.setRuntime(taskID, "duplicate_order", "存在重复订单，已停止。", "warn")
				return false
			}
			if result.Code == 100034 && result.PayMoney > 0 {
				m.applyPayMoneyUpdate(taskID, latestTask.PayMoney, result.PayMoney)
			}
			if !m.retryRunError(ctx, taskID, "创建订单失败："+err.Error(), checkedAt, interval) {
				return false
			}
			continue
		}
		if result.Code == 100079 {
			m.setRuntime(taskID, "duplicate_order", "存在重复订单，已停止。", "warn")
			return false
		}
		if result.OrderID == "" {
			if !m.retryRunError(ctx, taskID, "订单接口返回成功，但未返回订单 ID", checkedAt, interval) {
				return false
			}
			continue
		}
		payParam, ok := m.waitForPayParam(ctx, taskID, result.OrderID, cookie, interval, deadlineExceeded)
		if !ok {
			return false
		}
		task, log, err := m.store.SetTaskRuntime(context.Background(), taskID, model.TaskRuntimeUpdate{
			Status:                "waiting_payment",
			LastMessage:           "订单创建成功，请尽快完成支付。",
			OrderID:               result.OrderID,
			PaymentURL:            payParam.CodeURL,
			PaymentQRImageDataURL: payParam.QRImageDataURL,
			LastCheckedAt:         checkedAtText(),
		}, "info")
		if err == nil {
			m.publishTaskAndLog(task, log)
		}
		return true
	}
}

func (m *Manager) applyPayMoneyUpdate(taskID int64, oldPayMoney int64, newPayMoney int64) {
	if newPayMoney <= 0 || newPayMoney == oldPayMoney {
		return
	}
	message := fmt.Sprintf("createV2 返回订单金额更新，已将任务金额从 %d 分更新为 %d 分。", oldPayMoney, newPayMoney)
	task, log, err := m.store.SetTaskPayMoney(context.Background(), taskID, newPayMoney, message)
	if err == nil {
		m.publishTaskAndLog(task, log)
	}
}

func (m *Manager) retryRunError(ctx context.Context, taskID int64, message string, checkedAt string, interval time.Duration) bool {
	m.setRuntimeWithCheckedAt(taskID, "running", formatRetryMessage(message), "warn", checkedAt)
	return m.wait(ctx, interval)
}

func (m *Manager) waitForPayParam(ctx context.Context, taskID int64, orderID string, cookie string, interval time.Duration, deadlineExceeded func() bool) (biliticket.PayParamResult, bool) {
	for {
		if ctx.Err() != nil {
			return biliticket.PayParamResult{}, false
		}
		if deadlineExceeded != nil && deadlineExceeded() {
			m.setRuntime(taskID, "failed", "已超过任务结束时间，停止获取支付参数。", "warn")
			return biliticket.PayParamResult{}, false
		}
		payParam, err := m.ticket.GetPayParam(ctx, orderID, cookie)
		if err == nil {
			return payParam, true
		}
		if !m.retryRunError(ctx, taskID, "订单创建成功，但获取支付二维码失败："+err.Error(), checkedAtText(), interval) {
			return biliticket.PayParamResult{}, false
		}
	}
}

func checkedAtText() string {
	return time.Now().Format(time.RFC3339Nano)
}

func formatRetryMessage(message string) string {
	message = strings.TrimRight(strings.TrimSpace(message), "。")
	if message == "" {
		message = "运行异常"
	}
	return message + "，继续重试。"
}

func (m *Manager) setRuntime(taskID int64, status string, message string, level string) {
	m.setRuntimeWithCheckedAt(taskID, status, message, level, "")
}

func (m *Manager) setRuntimeWithCheckedAt(taskID int64, status string, message string, level string, checkedAt string) {
	task, log, err := m.store.SetTaskRuntime(context.Background(), taskID, model.TaskRuntimeUpdate{
		Status:        status,
		LastMessage:   message,
		LastCheckedAt: checkedAt,
	}, level)
	if err == nil {
		m.publishTaskAndLog(task, log)
	}
}

func (m *Manager) publishTaskAndLog(task model.Task, log model.TaskLog) {
	if m.hub == nil {
		return
	}
	m.hub.Publish("task.updated", task)
	if log.ID > 0 {
		m.hub.Publish("log.created", log)
	}
}

func (m *Manager) wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *Manager) waitUntilSaleStart(ctx context.Context, taskID int64, saleStart time.Time, timeOffset time.Duration) bool {
	nextReportAt := time.Now().Add(saleStartWaitReportInterval)
	warmedUp := false
	for {
		remaining := saleStart.Sub(nowWithOffset(timeOffset))
		if remaining <= 0 {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if !warmedUp && remaining <= saleStartWarmupBefore {
			warmedUp = true
			m.warmupShow(ctx, taskID)
		}
		now := time.Now()
		if !now.Before(nextReportAt) {
			m.setRuntime(taskID, "waiting_start", "等待起售中，距离起售还有 "+formatRemaining(remaining)+"。", "info")
			nextReportAt = now.Add(saleStartWaitReportInterval)
		}
		waitFor := remaining
		if waitFor > saleStartWaitTick {
			waitFor = saleStartWaitTick
		}
		if !m.wait(ctx, waitFor) {
			return false
		}
	}
}

// 预热连接
func (m *Manager) warmupShow(ctx context.Context, taskID int64) {
	if m.ticket == nil {
		return
	}
	message := fmt.Sprintf("距离起售不足 %d 秒，开始预热抢票连接。", int(saleStartWarmupBefore.Seconds()))
	m.setRuntime(taskID, "waiting_start", message, "info")
	if err := m.ticket.WarmupShow(ctx, saleStartWarmupRequestCount); err != nil {
		m.setRuntime(taskID, "waiting_start", "预热抢票连接失败："+err.Error(), "warn")
		return
	}
	m.setRuntime(taskID, "waiting_start", fmt.Sprintf("预热完成，已发送 %d 个 HEAD 请求。", saleStartWarmupRequestCount), "info")
}

func (m *Manager) syncTaskTime(ctx context.Context, task model.Task) (model.Task, error) {
	strategy := model.NormalizeTimeSyncStrategy(task.TimeSyncStrategy)
	if strategy == model.TimeSyncStrategyLocal {
		message := "时间同步策略：本地时间，offset=+0ms。"
		updated, log, err := m.store.SetTaskTimeSync(ctx, task.ID, strategy, 0, time.Now().Format(time.RFC3339), message)
		if err == nil {
			m.publishTaskAndLog(updated, log)
		}
		return updated, err
	}

	result, err := m.timeSync.Sync(ctx)
	if err != nil {
		updated, log, setErr := m.store.SetTaskRuntime(ctx, task.ID, model.TaskRuntimeUpdate{
			Status:      "failed",
			LastMessage: "时间同步失败：" + err.Error(),
		}, "error")
		if setErr == nil {
			m.publishTaskAndLog(updated, log)
		}
		return model.Task{}, err
	}

	message := fmt.Sprintf(
		"时间同步完成：哔哩哔哩时间 offset=%+dms，平均RTT=%dms，采样%d次后取中间%d次平均。",
		result.OffsetMillis,
		result.AverageRTTMillis,
		result.TotalSampleCount,
		result.AveragedSampleCount,
	)
	updated, log, err := m.store.SetTaskTimeSync(ctx, task.ID, strategy, result.OffsetMillis, result.SyncedAt.Format(time.RFC3339), message)
	if err == nil {
		m.publishTaskAndLog(updated, log)
	}
	return updated, err
}

func nowWithOffset(offset time.Duration) time.Time {
	return time.Now().Add(offset)
}

func (m *Manager) remove(taskID int64) {
	m.mu.Lock()
	delete(m.running, taskID)
	m.mu.Unlock()
}

func formatRemaining(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	totalSeconds := int(duration.Round(time.Second).Seconds())
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d小时%d分%d秒", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%d分%d秒", minutes, seconds)
	}
	return fmt.Sprintf("%d秒", seconds)
}

func validateTask(task model.Task) error {
	if task.AccountID <= 0 {
		return errors.New("请先选择账号")
	}
	if task.ProjectID <= 0 || task.ScreenID <= 0 || task.SKUID <= 0 {
		return errors.New("请先获取票务信息并选择票信息")
	}
	taskMode := model.NormalizeTaskMode(task.TaskMode)
	durationMode := model.NormalizeDurationMode(task.DurationMode)
	if taskMode == model.TaskModeRush && strings.TrimSpace(task.SaleStart) == "" {
		return errors.New("票档缺少起售时间")
	}
	if taskMode == model.TaskModeRestock && durationMode == model.DurationModeLimited {
		if _, err := parseTaskTime(task.EndAt); err != nil {
			return errors.New("回流捡漏有限模式需要设置合法截止时间")
		}
	}
	if len(task.BuyerInfo) == 0 {
		return errors.New("请至少选择一位实名购票人")
	}
	if task.DeliverInfo == nil || task.DeliverInfo.ID <= 0 {
		return errors.New("请先选择收货地址")
	}
	if strings.TrimSpace(task.Buyer) == "" {
		return errors.New("请填写联系人姓名")
	}
	if strings.TrimSpace(task.Tel) == "" {
		return errors.New("请填写联系人电话")
	}
	return nil
}

func parseTaskTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("时间为空")
	}
	formats := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04",
	}
	var lastErr error
	for _, format := range formats {
		parsed, err := time.ParseInLocation(format, value, time.Local)
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
