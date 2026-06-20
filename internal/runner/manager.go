package runner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fdcs99/biligo/internal/biliticket"
	"github.com/fdcs99/biligo/internal/events"
	"github.com/fdcs99/biligo/internal/model"
	"github.com/fdcs99/biligo/internal/notify"
	proxynet "github.com/fdcs99/biligo/internal/proxy"
	"github.com/fdcs99/biligo/internal/store"
	"github.com/fdcs99/biligo/internal/tickettime"
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
	notifier *notify.Service

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	wg      sync.WaitGroup
}

const (
	saleStartWaitTick             = time.Millisecond * 50 // 等待 50 ms
	saleStartWaitReportInterval   = time.Second
	hotProjectCheckBefore         = 5 * time.Minute
	hotProjectCheckAttempts       = 10
	hotProjectCheckRetryInterval  = 100 * time.Millisecond
	saleStartWarmupBefore         = 30 * time.Second
	saleStartWarmupRequestCount   = 5
	defaultProxyAPIPullBefore     = 5 * time.Minute
	createOrderAttemptsPerPrepare = 4
)

var pullKuaidailiDPS = proxynet.PullKuaidailiDPS
var noProxyCreate412RetryWait = 3 * time.Second

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
		notifier: notify.NewService(store, nil, hub),
		running:  map[int64]context.CancelFunc{},
	}
}

func (m *Manager) SetNotifier(notifier *notify.Service) {
	m.notifier = notifier
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

	taskMode := model.NormalizeTaskMode(task.TaskMode)
	if taskMode == model.TaskModeRush || taskMode == model.TaskModeHybrid {
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
	if taskMode == model.TaskModeRestock {
		status = "running"
		message = "回流捡漏任务已下发，开始检测票档状态。"
	} else if taskMode == model.TaskModeHybrid {
		message = "抢票+回流捡漏任务已下发，等待票档起售时间。"
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

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(runCtx, taskID, cookie)
	}()
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

func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.running))
	for taskID, cancel := range m.running {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
		delete(m.running, taskID)
	}
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	tasks, logs, err := m.store.PauseActiveTasks(ctx, "服务停止，任务已自动停止。")
	if err != nil {
		return err
	}
	for index := range tasks {
		m.publishTask(tasks[index])
		if index < len(logs) {
			m.publishLog(logs[index])
		}
	}

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) run(ctx context.Context, taskID int64, cookie string) {
	defer m.remove(taskID)

	task, err := m.store.GetTask(context.Background(), taskID)
	if err != nil {
		return
	}
	switch model.NormalizeTaskMode(task.TaskMode) {
	case model.TaskModeRestock:
		m.runRestock(ctx, taskID, cookie, task)
		return
	case model.TaskModeHybrid:
		m.runHybrid(ctx, taskID, cookie, task)
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
	proxyRuntime, err := m.newTaskProxyRuntime(ctx, taskID, task)
	if err != nil {
		m.setRuntime(taskID, "failed", "代理组初始化失败："+err.Error(), "error")
		return
	}
	endAt := saleStart.Add(10 * time.Minute)
	if parsedEnd, err := parseTaskTime(task.EndAt); err == nil && !parsedEnd.IsZero() {
		endAt = parsedEnd
	}
	timeOffset := time.Duration(task.TimeOffsetMillis) * time.Millisecond

	if !m.waitUntilSaleStart(ctx, taskID, saleStart, timeOffset, proxyRuntime, func() bool {
		updated, ok := m.refreshHotProjectBeforeSaleStart(ctx, taskID, task, cookie)
		task = updated
		return ok
	}) {
		return
	}
	if proxyRuntime != nil {
		if err := proxyRuntime.ensureReady(ctx); err != nil {
			m.setRuntime(taskID, "failed", "代理组无可用节点："+err.Error(), "error")
			return
		}
	}

	m.setRuntime(taskID, "running", "已到起售时间，开始准备订单。", "info")
	interval := time.Duration(task.PollIntervalMillis) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}

	deadlineExceeded := func() bool {
		return nowWithOffset(timeOffset).After(endAt)
	}
	if isConcurrentProxyTask(task) {
		m.runConcurrentOrderFlow(ctx, taskID, cookie, interval, deadlineExceeded, nil, "failed", "已超过任务结束时间，停止检测。", "warn", "已超过任务结束时间，停止获取支付参数。", proxyRuntime)
		return
	}
	m.runOrderFlow(ctx, taskID, cookie, interval, deadlineExceeded, proxyRuntime)
}

func (m *Manager) runHybrid(ctx context.Context, taskID int64, cookie string, task model.Task) {
	saleStart, err := parseTaskTime(task.SaleStart)
	if err != nil {
		m.setRuntime(taskID, "failed", "无法解析票档起售时间："+err.Error(), "error")
		return
	}
	proxyRuntime, err := m.newTaskProxyRuntime(ctx, taskID, task)
	if err != nil {
		m.setRuntime(taskID, "failed", "代理组初始化失败："+err.Error(), "error")
		return
	}
	rushDuration := time.Duration(task.RushDurationSeconds) * time.Second
	if rushDuration <= 0 {
		rushDuration = time.Duration(model.DefaultRushDurationSeconds) * time.Second
	}
	timeOffset := time.Duration(task.TimeOffsetMillis) * time.Millisecond

	if !m.waitUntilSaleStart(ctx, taskID, saleStart, timeOffset, proxyRuntime, func() bool {
		updated, ok := m.refreshHotProjectBeforeSaleStart(ctx, taskID, task, cookie)
		task = updated
		return ok
	}) {
		return
	}
	if proxyRuntime != nil {
		if err := proxyRuntime.ensureReady(ctx); err != nil {
			m.setRuntime(taskID, "failed", "代理组无可用节点："+err.Error(), "error")
			return
		}
	}

	m.setRuntime(taskID, "running", fmt.Sprintf("已到起售时间，开始抢票；%d 秒后切换回流捡漏。", int(rushDuration.Seconds())), "info")
	interval := time.Duration(task.PollIntervalMillis) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}

	switchMessage := "抢票窗口结束，切换回流捡漏。"
	var rushStartedAt time.Time
	var rushStartOnce sync.Once
	markRushStarted := func() {
		rushStartOnce.Do(func() {
			rushStartedAt = nowWithOffset(timeOffset)
		})
	}
	deadlineExceeded := func() bool {
		if rushStartedAt.IsZero() {
			return false
		}
		return !nowWithOffset(timeOffset).Before(rushStartedAt.Add(rushDuration))
	}
	var finished bool
	if isConcurrentProxyTask(task) {
		finished = m.runConcurrentOrderFlow(ctx, taskID, cookie, interval, deadlineExceeded, markRushStarted, "running", switchMessage, "info", switchMessage, proxyRuntime)
	} else {
		finished = m.runOrderFlowWithDeadline(ctx, taskID, cookie, interval, deadlineExceeded, markRushStarted, "running", switchMessage, "info", switchMessage, proxyRuntime)
	}
	if finished {
		return
	}
	if ctx.Err() != nil {
		return
	}
	latestTask, err := m.store.GetTask(context.Background(), taskID)
	if err != nil {
		return
	}
	if latestTask.Status == "running" && latestTask.LastMessage == switchMessage {
		m.runRestock(ctx, taskID, cookie, latestTask)
	}
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

		option, checkedCount, available, err := m.ticket.CheckSelectedTicketsStatus(ctx, latestTask, cookie)
		if err != nil {
			message := formatRetryMessage("票种状态检测失败：" + err.Error())
			m.setRuntimeWithCheckedAt(taskID, "running", message, "warn", checkedAt)
			if !m.wait(ctx, interval) {
				return
			}
			continue
		}

		if available {
			matchedTask, log, err := m.store.SetTaskMatchedTicket(context.Background(), taskID, option, latestTask.Quantity, "检测到票档可购买，开始准备订单。", checkedAt)
			if err != nil {
				m.setRuntimeWithCheckedAt(taskID, "running", formatRetryMessage("更新命中票种失败："+err.Error()), "warn", checkedAt)
			} else {
				m.publishTaskAndLog(matchedTask, log)
				switch m.runOrderAttempt(ctx, taskID, cookie, deadlineExceeded, nil) {
				case orderAttemptFinished, orderAttemptStopped:
					return
				}
			}
			if !m.wait(ctx, interval) {
				return
			}
			continue
		}
		m.setRuntimeWithCheckedAt(taskID, "running", fmt.Sprintf("已检测 %d 个已选票种，暂不可购买，继续检测。", checkedCount), "info", checkedAt)
		if !m.wait(ctx, interval) {
			return
		}
	}
}

func (m *Manager) runOrderFlow(ctx context.Context, taskID int64, cookie string, interval time.Duration, deadlineExceeded func() bool, proxyRuntime *taskProxyRuntime) bool {
	return m.runOrderFlowWithDeadline(ctx, taskID, cookie, interval, deadlineExceeded, nil, "failed", "已超过任务结束时间，停止检测。", "warn", "已超过任务结束时间，停止获取支付参数。", proxyRuntime)
}

func (m *Manager) runOrderFlowWithDeadline(ctx context.Context, taskID int64, cookie string, interval time.Duration, deadlineExceeded func() bool, beforeAttempt func(), deadlineStatus string, deadlineMessage string, deadlineLevel string, payParamDeadlineMessage string, proxyRuntime *taskProxyRuntime) bool {
orderLoop:
	for {
		if ctx.Err() != nil {
			return false
		}
		if deadlineExceeded != nil && deadlineExceeded() {
			m.setRuntime(taskID, deadlineStatus, deadlineMessage, deadlineLevel)
			return false
		}

		latestTask, err := m.store.GetTask(context.Background(), taskID)
		if err != nil {
			return false
		}
		checkedAt := checkedAtText()

		if beforeAttempt != nil {
			beforeAttempt()
		}
		client := m.ticketClient(proxyRuntime)
		prepared, err := client.PrepareOrder(ctx, latestTask, cookie)
		if err != nil {
			if m.switchProxyOnRequestError(ctx, taskID, proxyRuntime, err) {
				continue
			}
			if !m.retryRunError(ctx, taskID, "订单准备失败："+err.Error(), checkedAt, interval) {
				return false
			}
			continue
		}

		var result biliticket.OrderCreateResult
		var createErr error
		createFailedMessage := ""
		createRetryInterval := interval
		for createAttempt := 1; createAttempt <= createOrderAttemptsPerPrepare; createAttempt++ {
			if ctx.Err() != nil {
				return false
			}
			if deadlineExceeded != nil && deadlineExceeded() {
				m.setRuntime(taskID, deadlineStatus, deadlineMessage, deadlineLevel)
				return false
			}

			result, createErr = client.CreateOrder(ctx, latestTask, cookie, prepared)
			if createErr != nil {
				if result.Code == 100079 {
					m.setRuntime(taskID, "duplicate_order", "存在重复订单，已停止。", "warn")
					return false
				}
				if result.Code == 100034 && result.PayMoney > 0 {
					m.applyPayMoneyUpdate(taskID, latestTask.PayMoney, result.PayMoney)
					continue orderLoop
				}
				if shouldSwitchProxyForCreateError(result, createErr) {
					if m.switchProxy(ctx, taskID, proxyRuntime, "创建订单触发代理切换："+createErr.Error()) {
						continue orderLoop
					}
					createFailedMessage = "创建订单失败：" + createErr.Error()
					createRetryInterval = createErrorRetryInterval(result, proxyRuntime, interval)
					break
				}
				createFailedMessage = "创建订单失败：" + createErr.Error()
				continue
			}
			if result.Code == 100079 {
				m.setRuntime(taskID, "duplicate_order", "存在重复订单，已停止。", "warn")
				return false
			}
			if result.OrderID == "" {
				createFailedMessage = "订单接口返回成功，但未返回订单 ID"
				continue
			}

			break
		}
		if createErr != nil || result.OrderID == "" {
			if createFailedMessage == "" {
				createFailedMessage = "创建订单失败：未知错误"
			}
			if !m.retryRunError(ctx, taskID, createFailedMessage, checkedAt, createRetryInterval) {
				return false
			}
			continue
		}

		payParam, ok := m.waitForPayParam(ctx, taskID, result.OrderID, cookie, interval, deadlineExceeded, deadlineStatus, payParamDeadlineMessage, deadlineLevel, proxyRuntime)
		if !ok {
			return false
		}
		m.completeOrderSuccess(taskID, result.OrderID, payParam, checkedAtText())
		return true
	}
}

func (m *Manager) completeOrderSuccess(taskID int64, orderID string, payParam biliticket.PayParamResult, checkedAt string) (model.Task, bool) {
	task, log, err := m.store.SetTaskRuntime(context.Background(), taskID, model.TaskRuntimeUpdate{
		Status:                "waiting_payment",
		LastMessage:           "订单创建成功，请尽快完成支付。",
		OrderID:               orderID,
		PaymentURL:            payParam.CodeURL,
		PaymentQRImageDataURL: payParam.QRImageDataURL,
		LastCheckedAt:         checkedAt,
	}, "info")
	if err != nil {
		return model.Task{}, false
	}
	m.publishTaskAndLog(task, log)
	m.notifyTaskSuccess(task)
	return task, true
}

func (m *Manager) runConcurrentOrderFlow(ctx context.Context, taskID int64, cookie string, interval time.Duration, deadlineExceeded func() bool, beforeAttempt func(), deadlineStatus string, deadlineMessage string, deadlineLevel string, payParamDeadlineMessage string, proxyRuntime *taskProxyRuntime) bool {
	if proxyRuntime == nil {
		return m.runOrderFlowWithDeadline(ctx, taskID, cookie, interval, deadlineExceeded, beforeAttempt, deadlineStatus, deadlineMessage, deadlineLevel, payParamDeadlineMessage, nil)
	}
	if err := proxyRuntime.ensureReady(ctx); err != nil {
		m.setRuntime(taskID, "failed", "代理组无可用节点："+err.Error(), "error")
		return false
	}
	nodes := append([]model.ProxyNode(nil), proxyRuntime.nodes...)
	if len(nodes) == 0 {
		m.setRuntime(taskID, "failed", "代理组无可用节点：代理组没有节点", "error")
		return false
	}

	m.setRuntime(taskID, "running", fmt.Sprintf("并发代理已启动 %d 个抢票线程。", len(nodes)), "info")
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var finishOnce sync.Once
	var stateMu sync.Mutex
	var terminal atomic.Bool
	var finished bool
	var success bool

	finishTerminal := func(status string, message string, level string) {
		finishOnce.Do(func() {
			stateMu.Lock()
			defer stateMu.Unlock()
			terminal.Store(true)
			finished = true
			m.setRuntime(taskID, status, message, level)
			cancel()
		})
	}
	finishSuccess := func(orderID string, payParam biliticket.PayParamResult, checkedAt string) {
		finishOnce.Do(func() {
			stateMu.Lock()
			defer stateMu.Unlock()
			terminal.Store(true)
			if _, ok := m.completeOrderSuccess(taskID, orderID, payParam, checkedAt); ok {
				success = true
			}
			finished = true
			cancel()
		})
	}
	setWorkerRuntime := func(message string, level string, checkedAt string) bool {
		stateMu.Lock()
		defer stateMu.Unlock()
		if terminal.Load() {
			return false
		}
		m.setRuntimeWithCheckedAt(taskID, "running", message, level, checkedAt)
		return true
	}

	workerCount := 0
	for _, node := range nodes {
		node := node
		runtime, err := m.fixedProxyRuntimeForNode(taskID, proxyRuntime.group, node)
		if err != nil {
			m.setRuntime(taskID, "running", fmt.Sprintf("代理节点 %s 初始化失败：%s", proxyNodeLabel(node), err.Error()), "warn")
			continue
		}
		workerCount++
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.runConcurrentProxyWorker(workerCtx, taskID, cookie, interval, deadlineExceeded, beforeAttempt, deadlineStatus, deadlineMessage, deadlineLevel, payParamDeadlineMessage, runtime, terminal.Load, setWorkerRuntime, finishTerminal, finishSuccess)
		}()
	}

	if workerCount == 0 {
		m.setRuntime(taskID, "failed", "代理组无可用节点：代理节点初始化失败", "error")
		return false
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		cancel()
		<-done
	}
	if finished {
		return success
	}
	return false
}

func (m *Manager) fixedProxyRuntimeForNode(taskID int64, group model.ProxyGroup, node model.ProxyNode) (*taskProxyRuntime, error) {
	client, err := proxynet.NewHTTPClient(node)
	if err != nil {
		return nil, err
	}
	return &taskProxyRuntime{
		manager: m,
		taskID:  taskID,
		group:   group,
		nodes:   []model.ProxyNode{node},
		index:   0,
		client:  m.ticket.WithHTTPClient(client),
		fixed:   true,
	}, nil
}

func (m *Manager) runConcurrentProxyWorker(ctx context.Context, taskID int64, cookie string, interval time.Duration, deadlineExceeded func() bool, beforeAttempt func(), deadlineStatus string, deadlineMessage string, deadlineLevel string, payParamDeadlineMessage string, proxyRuntime *taskProxyRuntime, terminalReached func() bool, setWorkerRuntime func(string, string, string) bool, finishTerminal func(string, string, string), finishSuccess func(string, biliticket.PayParamResult, string)) {
	node := proxyRuntime.currentNode()
	prefix := "代理节点 " + proxyNodeLabel(node) + "："
prepareLoop:
	for {
		if ctx.Err() != nil || terminalReached() {
			return
		}
		if deadlineExceeded != nil && deadlineExceeded() {
			finishTerminal(deadlineStatus, deadlineMessage, deadlineLevel)
			return
		}

		latestTask, err := m.store.GetTask(context.Background(), taskID)
		if err != nil {
			return
		}
		checkedAt := checkedAtText()

		if beforeAttempt != nil {
			beforeAttempt()
		}
		client := m.ticketClient(proxyRuntime)
		prepared, err := client.PrepareOrder(ctx, latestTask, cookie)
		if err != nil {
			if !m.retryConcurrentRunError(ctx, formatRetryMessage(prefix+"订单准备失败："+err.Error()), checkedAt, interval, terminalReached, setWorkerRuntime) {
				return
			}
			continue
		}

		var result biliticket.OrderCreateResult
		var createErr error
		createFailedMessage := ""
		for createAttempt := 1; createAttempt <= createOrderAttemptsPerPrepare; createAttempt++ {
			if ctx.Err() != nil || terminalReached() {
				return
			}
			if deadlineExceeded != nil && deadlineExceeded() {
				finishTerminal(deadlineStatus, deadlineMessage, deadlineLevel)
				return
			}

			result, createErr = client.CreateOrder(ctx, latestTask, cookie, prepared)
			if createErr != nil {
				if result.Code == 100079 {
					finishTerminal("duplicate_order", prefix+"存在重复订单，已停止。", "warn")
					return
				}
				if result.Code == 100034 && result.PayMoney > 0 {
					m.applyPayMoneyUpdate(taskID, latestTask.PayMoney, result.PayMoney)
					continue prepareLoop
				}
				createFailedMessage = prefix + "创建订单失败：" + createErr.Error()
				continue
			}
			if result.Code == 100079 {
				finishTerminal("duplicate_order", prefix+"存在重复订单，已停止。", "warn")
				return
			}
			if result.OrderID == "" {
				createFailedMessage = prefix + "订单接口返回成功，但未返回订单 ID"
				continue
			}
			break
		}
		if createErr != nil || result.OrderID == "" {
			if createFailedMessage == "" {
				createFailedMessage = prefix + "创建订单失败：未知错误"
			}
			if !m.retryConcurrentRunError(ctx, formatRetryMessage(createFailedMessage), checkedAt, interval, terminalReached, setWorkerRuntime) {
				return
			}
			continue
		}

		payParam, ok := m.waitForConcurrentPayParam(ctx, result.OrderID, cookie, interval, deadlineExceeded, deadlineStatus, payParamDeadlineMessage, deadlineLevel, proxyRuntime, prefix, terminalReached, setWorkerRuntime, finishTerminal)
		if !ok {
			return
		}
		finishSuccess(result.OrderID, payParam, checkedAtText())
		return
	}
}

func (m *Manager) retryConcurrentRunError(ctx context.Context, message string, checkedAt string, interval time.Duration, terminalReached func() bool, setWorkerRuntime func(string, string, string) bool) bool {
	if ctx.Err() != nil || terminalReached() {
		return false
	}
	if !setWorkerRuntime(message, "warn", checkedAt) {
		return false
	}
	return m.wait(ctx, interval)
}

func (m *Manager) waitForConcurrentPayParam(ctx context.Context, orderID string, cookie string, interval time.Duration, deadlineExceeded func() bool, deadlineStatus string, deadlineMessage string, deadlineLevel string, proxyRuntime *taskProxyRuntime, prefix string, terminalReached func() bool, setWorkerRuntime func(string, string, string) bool, finishTerminal func(string, string, string)) (biliticket.PayParamResult, bool) {
	for {
		if ctx.Err() != nil || terminalReached() {
			return biliticket.PayParamResult{}, false
		}
		if deadlineExceeded != nil && deadlineExceeded() {
			finishTerminal(deadlineStatus, deadlineMessage, deadlineLevel)
			return biliticket.PayParamResult{}, false
		}
		payParam, err := m.ticketClient(proxyRuntime).GetPayParam(ctx, orderID, cookie)
		if err == nil {
			return payParam, true
		}
		if !m.retryConcurrentRunError(ctx, formatRetryMessage(prefix+"订单创建成功，但获取支付二维码失败："+err.Error()), checkedAtText(), interval, terminalReached, setWorkerRuntime) {
			return biliticket.PayParamResult{}, false
		}
	}
}

type orderAttemptResult int

const (
	orderAttemptRetryDetection orderAttemptResult = iota
	orderAttemptFinished
	orderAttemptStopped
)

func (m *Manager) runOrderAttempt(ctx context.Context, taskID int64, cookie string, deadlineExceeded func() bool, proxyRuntime *taskProxyRuntime) orderAttemptResult {
prepareLoop:
	for {
		if ctx.Err() != nil {
			return orderAttemptStopped
		}
		if deadlineExceeded != nil && deadlineExceeded() {
			m.setRuntime(taskID, "failed", "已超过任务结束时间，停止检测。", "warn")
			return orderAttemptStopped
		}

		latestTask, err := m.store.GetTask(context.Background(), taskID)
		if err != nil {
			return orderAttemptStopped
		}
		checkedAt := checkedAtText()

		client := m.ticketClient(proxyRuntime)
		prepared, err := client.PrepareOrder(ctx, latestTask, cookie)
		if err != nil {
			m.setRuntimeWithCheckedAt(taskID, "running", formatReturnToDetectMessage("订单准备失败："+err.Error()), "warn", checkedAt)
			return orderAttemptRetryDetection
		}

		var result biliticket.OrderCreateResult
		var createErr error
		createFailedMessage := ""
		for createAttempt := 1; createAttempt <= createOrderAttemptsPerPrepare; createAttempt++ {
			if ctx.Err() != nil {
				return orderAttemptStopped
			}
			if deadlineExceeded != nil && deadlineExceeded() {
				m.setRuntime(taskID, "failed", "已超过任务结束时间，停止检测。", "warn")
				return orderAttemptStopped
			}

			result, createErr = client.CreateOrder(ctx, latestTask, cookie, prepared)
			if createErr != nil {
				if result.Code == 100079 {
					m.setRuntime(taskID, "duplicate_order", "存在重复订单，已停止。", "warn")
					return orderAttemptStopped
				}
				if result.Code == 100034 && result.PayMoney > 0 {
					m.applyPayMoneyUpdate(taskID, latestTask.PayMoney, result.PayMoney)
					continue prepareLoop
				}
				if shouldWaitForNoProxyCreate412(result, proxyRuntime) {
					m.setRuntimeWithCheckedAt(taskID, "running", formatRetryMessage("创建订单失败："+createErr.Error()), "warn", checkedAt)
					if !m.wait(ctx, noProxyCreate412RetryWait) {
						return orderAttemptStopped
					}
					continue prepareLoop
				}
				createFailedMessage = "创建订单失败：" + createErr.Error()
				continue
			}
			if result.Code == 100079 {
				m.setRuntime(taskID, "duplicate_order", "存在重复订单，已停止。", "warn")
				return orderAttemptStopped
			}
			if result.OrderID == "" {
				createFailedMessage = "订单接口返回成功，但未返回订单 ID"
				continue
			}

			interval := time.Duration(latestTask.PollIntervalMillis) * time.Millisecond
			if interval <= 0 {
				interval = time.Second
			}
			payParam, ok := m.waitForPayParam(ctx, taskID, result.OrderID, cookie, interval, deadlineExceeded, "failed", "已超过任务结束时间，停止获取支付参数。", "warn", proxyRuntime)
			if !ok {
				return orderAttemptStopped
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
				m.notifyTaskSuccess(task)
			}
			return orderAttemptFinished
		}
		if createErr != nil || result.OrderID == "" {
			if createFailedMessage == "" {
				createFailedMessage = "创建订单失败：未知错误"
			}
			m.setRuntimeWithCheckedAt(taskID, "running", formatReturnToDetectMessage(createFailedMessage), "warn", checkedAt)
			return orderAttemptRetryDetection
		}
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

type taskProxyRuntime struct {
	manager *Manager
	taskID  int64
	group   model.ProxyGroup
	nodes   []model.ProxyNode
	index   int
	client  *biliticket.Client
	pulled  bool
	triedAt time.Time
	fixed   bool
}

func isConcurrentProxyTask(task model.Task) bool {
	taskMode := model.NormalizeTaskMode(task.TaskMode)
	return task.ProxyGroupID > 0 &&
		model.NormalizeProxyMode(task.ProxyMode) == model.ProxyModeConcurrent &&
		(taskMode == model.TaskModeRush || taskMode == model.TaskModeHybrid)
}

func (m *Manager) newTaskProxyRuntime(ctx context.Context, taskID int64, task model.Task) (*taskProxyRuntime, error) {
	if task.ProxyGroupID <= 0 || model.NormalizeTaskMode(task.TaskMode) == model.TaskModeRestock {
		return nil, nil
	}
	group, err := m.store.GetProxyGroup(ctx, task.ProxyGroupID)
	if err != nil {
		return nil, err
	}
	runtime := &taskProxyRuntime{
		manager: m,
		taskID:  taskID,
		group:   group,
		index:   -1,
	}
	if group.Type == model.ProxyGroupTypeAPI {
		return runtime, nil
	}
	if err := runtime.reloadNodes(ctx); err != nil {
		return nil, err
	}
	if err := runtime.activateIndex(0); err != nil {
		return nil, err
	}
	m.setRuntime(taskID, "waiting_start", "已启用代理组："+group.Name+"。", "info")
	return runtime, nil
}

func (p *taskProxyRuntime) shouldPull(remaining time.Duration) bool {
	if p == nil || p.group.Type != model.ProxyGroupTypeAPI || p.pulled || remaining > p.pullBefore() {
		return false
	}
	return p.triedAt.IsZero() || time.Since(p.triedAt) >= 10*time.Second
}

func (p *taskProxyRuntime) pullBefore() time.Duration {
	if p == nil || p.group.APIConfig == nil {
		return defaultProxyAPIPullBefore
	}
	raw := strings.TrimSpace(p.group.APIConfig["pullBeforeMinutes"])
	if raw == "" {
		return defaultProxyAPIPullBefore
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes <= 0 {
		return defaultProxyAPIPullBefore
	}
	return time.Duration(minutes) * time.Minute
}

func (p *taskProxyRuntime) pullAndActivate(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.group.Type != model.ProxyGroupTypeAPI {
		return p.ensureReady(ctx)
	}
	if p.group.APIProvider != model.ProxyProviderKuaidailiDPS {
		return errors.New("API 代理组仅支持快代理私密代理")
	}
	p.triedAt = time.Now()
	pullCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	nodes, err := pullKuaidailiDPS(pullCtx, p.group)
	cancel()
	if err != nil {
		_, _ = p.manager.store.SetProxyGroupPullResult(context.Background(), p.group.ID, "error", err.Error())
		return err
	}
	if _, err := p.manager.store.ReplaceAPIProxyNodes(context.Background(), p.group.ID, nodes); err != nil {
		return err
	}
	group, err := p.manager.store.SetProxyGroupPullResult(context.Background(), p.group.ID, "success", fmt.Sprintf("已拉取 %d 个代理节点。", len(nodes)))
	if err == nil {
		p.group = group
	}
	p.pulled = true
	if err := p.reloadNodes(ctx); err != nil {
		return err
	}
	return p.activateIndex(0)
}

func (p *taskProxyRuntime) ensureReady(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.client != nil {
		return nil
	}
	if p.group.Type == model.ProxyGroupTypeAPI && !p.pulled {
		if err := p.pullAndActivate(ctx); err != nil {
			return err
		}
		p.manager.setRuntime(p.taskID, "waiting_start", proxyAPIReadyMessage(p.nodes), "info")
		return nil
	}
	if err := p.reloadNodes(ctx); err != nil {
		return err
	}
	return p.activateIndex(0)
}

func (p *taskProxyRuntime) reloadNodes(ctx context.Context) error {
	nodes, err := p.manager.store.ListProxyNodes(ctx, p.group.ID)
	if err != nil {
		return err
	}
	available := make([]model.ProxyNode, 0, len(nodes))
	for _, node := range nodes {
		if node.LastTestStatus == "error" {
			continue
		}
		available = append(available, node)
	}
	if len(available) == 0 {
		available = nodes
	}
	if len(available) == 0 {
		return errors.New("代理组没有节点")
	}
	p.nodes = available
	return nil
}

func (p *taskProxyRuntime) activateIndex(index int) error {
	if p == nil {
		return nil
	}
	if len(p.nodes) == 0 {
		return errors.New("代理组没有节点")
	}
	if index < 0 {
		index = 0
	}
	index = index % len(p.nodes)
	client, err := proxynet.NewHTTPClient(p.nodes[index])
	if err != nil {
		return err
	}
	p.index = index
	p.client = p.manager.ticket.WithHTTPClient(client)
	return nil
}

func (p *taskProxyRuntime) currentNode() model.ProxyNode {
	if p == nil || p.index < 0 || p.index >= len(p.nodes) {
		return model.ProxyNode{}
	}
	return p.nodes[p.index]
}

func (p *taskProxyRuntime) switchNext(ctx context.Context, reason string) error {
	if p == nil {
		return errors.New("任务未启用代理组")
	}
	if p.fixed {
		return errors.New("并发代理线程固定使用当前节点")
	}
	if len(p.nodes) < 2 {
		return errors.New("代理组没有可切换的下一个节点")
	}
	current := p.currentNode()
	if current.ID > 0 && strings.TrimSpace(reason) != "" {
		_, _ = p.manager.store.SetProxyNodeTestResult(context.Background(), current.ID, "error", reason, 0, "")
	}
	next := p.index + 1
	if next >= len(p.nodes) {
		next = 0
	}
	if err := p.activateIndex(next); err != nil {
		if err := p.reloadNodes(ctx); err != nil {
			return err
		}
		return p.activateIndex(0)
	}
	return nil
}

func (m *Manager) ticketClient(proxyRuntime *taskProxyRuntime) *biliticket.Client {
	if proxyRuntime != nil && proxyRuntime.client != nil {
		return proxyRuntime.client
	}
	return m.ticket
}

func shouldSwitchProxyForCreateError(result biliticket.OrderCreateResult, err error) bool {
	if result.Code == 412 || result.Code == 3 {
		return true
	}
	return proxynet.IsRequestError(err)
}

func shouldWaitForNoProxyCreate412(result biliticket.OrderCreateResult, proxyRuntime *taskProxyRuntime) bool {
	return proxyRuntime == nil && result.Code == 412
}

func createErrorRetryInterval(result biliticket.OrderCreateResult, proxyRuntime *taskProxyRuntime, fallback time.Duration) time.Duration {
	if shouldWaitForNoProxyCreate412(result, proxyRuntime) {
		return noProxyCreate412RetryWait
	}
	return fallback
}

func (m *Manager) switchProxyOnRequestError(ctx context.Context, taskID int64, proxyRuntime *taskProxyRuntime, err error) bool {
	if !proxynet.IsRequestError(err) {
		return false
	}
	return m.switchProxy(ctx, taskID, proxyRuntime, "代理请求失败："+err.Error())
}

func (m *Manager) switchProxy(ctx context.Context, taskID int64, proxyRuntime *taskProxyRuntime, reason string) bool {
	if proxyRuntime == nil {
		return false
	}
	if proxyRuntime.fixed {
		return false
	}
	if err := proxyRuntime.switchNext(ctx, reason); err != nil {
		m.setRuntime(taskID, "running", "代理切换失败："+err.Error()+"，继续使用当前节点重试。", "warn")
		return false
	}
	node := proxyRuntime.currentNode()
	m.setRuntime(taskID, "running", fmt.Sprintf("%s，已切换到代理节点 %s。", strings.TrimRight(reason, "。"), proxyNodeLabel(node)), "warn")
	return true
}

func proxyNodeLabel(node model.ProxyNode) string {
	name := strings.TrimSpace(node.Name)
	if name != "" {
		return name
	}
	if node.Host != "" && node.Port > 0 {
		return proxyNodeAddress(node)
	}
	return "未知节点"
}

func proxyNodeAddress(node model.ProxyNode) string {
	if node.Host != "" && node.Port > 0 {
		return fmt.Sprintf("%s:%d", node.Host, node.Port)
	}
	return "未知地址"
}

func proxyAPIReadyMessage(nodes []model.ProxyNode) string {
	if len(nodes) == 0 {
		return "代理 API 拉取完成，但未准备代理节点。"
	}
	labels := make([]string, 0, len(nodes))
	for _, node := range nodes {
		labels = append(labels, proxyNodeAddress(node))
	}
	return fmt.Sprintf("代理 API 拉取完成，已准备 %d 个代理节点：%s。", len(nodes), strings.Join(labels, "、"))
}

func (m *Manager) retryRunError(ctx context.Context, taskID int64, message string, checkedAt string, interval time.Duration) bool {
	m.setRuntimeWithCheckedAt(taskID, "running", formatRetryMessage(message), "warn", checkedAt)
	return m.wait(ctx, interval)
}

func (m *Manager) waitForPayParam(ctx context.Context, taskID int64, orderID string, cookie string, interval time.Duration, deadlineExceeded func() bool, deadlineStatus string, deadlineMessage string, deadlineLevel string, proxyRuntime *taskProxyRuntime) (biliticket.PayParamResult, bool) {
	for {
		if ctx.Err() != nil {
			return biliticket.PayParamResult{}, false
		}
		if deadlineExceeded != nil && deadlineExceeded() {
			m.setRuntime(taskID, deadlineStatus, deadlineMessage, deadlineLevel)
			return biliticket.PayParamResult{}, false
		}
		payParam, err := m.ticketClient(proxyRuntime).GetPayParam(ctx, orderID, cookie)
		if err == nil {
			return payParam, true
		}
		if m.switchProxyOnRequestError(ctx, taskID, proxyRuntime, err) {
			continue
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

func formatReturnToDetectMessage(message string) string {
	message = strings.TrimRight(strings.TrimSpace(message), "。")
	if message == "" {
		message = "运行异常"
	}
	return message + "，返回票种检测。"
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
	m.publishTask(task)
	m.publishLog(log)
}

func (m *Manager) publishTask(task model.Task) {
	if m.hub != nil {
		m.hub.Publish("task.updated", task)
	}
}

func (m *Manager) publishLog(log model.TaskLog) {
	if m.hub != nil && log.ID > 0 {
		m.hub.Publish("log.created", log)
	}
}

func (m *Manager) notifyTaskSuccess(task model.Task) {
	if m.notifier != nil {
		m.notifier.SendTaskSuccess(context.Background(), task)
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

func (m *Manager) waitUntilSaleStart(ctx context.Context, taskID int64, saleStart time.Time, timeOffset time.Duration, proxyRuntime *taskProxyRuntime, hotProjectCheck func() bool) bool {
	nextReportAt := time.Now().Add(saleStartWaitReportInterval)
	hotProjectChecked := false
	warmedUp := false
	for {
		remaining := saleStart.Sub(nowWithOffset(timeOffset))
		if remaining <= 0 {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if !hotProjectChecked && remaining <= hotProjectCheckBefore {
			hotProjectChecked = true
			if hotProjectCheck != nil && !hotProjectCheck() {
				return false
			}
		}
		if proxyRuntime != nil && proxyRuntime.shouldPull(remaining) {
			if err := proxyRuntime.pullAndActivate(ctx); err != nil {
				m.setRuntime(taskID, "waiting_start", "代理 API 拉取失败："+err.Error(), "warn")
			} else {
				m.setRuntime(taskID, "waiting_start", proxyAPIReadyMessage(proxyRuntime.nodes), "info")
			}
		}
		if !warmedUp && remaining <= saleStartWarmupBefore {
			warmedUp = true
			if proxyRuntime != nil {
				if err := proxyRuntime.ensureReady(ctx); err != nil {
					m.setRuntime(taskID, "failed", "预热前准备代理失败："+err.Error(), "error")
					return false
				}
			}
			m.warmupShow(ctx, taskID, proxyRuntime)
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

func (m *Manager) refreshHotProjectBeforeSaleStart(ctx context.Context, taskID int64, task model.Task, cookie string) (model.Task, bool) {
	if m.ticket == nil {
		return task, true
	}

	m.setRuntime(taskID, "waiting_start", fmt.Sprintf("开票前 5 分钟开始校验 hot_project 状态，当前本地状态为 %t。", task.IsHotProject), "info")
	var lastErr error
	for attempt := 1; attempt <= hotProjectCheckAttempts; attempt++ {
		isHotProject, err := m.ticket.FetchProjectHotProject(ctx, task.ProjectID, cookie)
		if err == nil {
			if isHotProject == task.IsHotProject {
				m.setRuntime(taskID, "waiting_start", fmt.Sprintf("开票前 hot_project 状态校验完成，远端状态与本地一致：%t。", isHotProject), "info")
				return task, true
			}
			message := fmt.Sprintf("开票前检测到 hot_project 状态从 %t 变为 %t，已更新任务并重新下发。", task.IsHotProject, isHotProject)
			updated, log, err := m.store.SetTaskHotProject(context.Background(), taskID, isHotProject, message)
			if err != nil {
				m.setRuntime(taskID, "waiting_start", "更新 hot_project 状态失败："+err.Error()+"，继续使用本地状态。", "warn")
				return task, true
			}
			m.publishTaskAndLog(updated, log)
			return updated, true
		}
		lastErr = err
		if attempt < hotProjectCheckAttempts && !m.wait(ctx, hotProjectCheckRetryInterval) {
			return task, false
		}
	}

	message := fmt.Sprintf("开票前 hot_project 状态校验连续失败 %d 次，继续使用本地状态。", hotProjectCheckAttempts)
	if lastErr != nil {
		message += "最后错误：" + lastErr.Error()
	}
	m.setRuntime(taskID, "waiting_start", message, "warn")
	return task, true
}

// 预热连接
func (m *Manager) warmupShow(ctx context.Context, taskID int64, proxyRuntime *taskProxyRuntime) {
	if m.ticket == nil {
		return
	}
	message := fmt.Sprintf("距离起售不足 %d 秒，开始预热抢票连接。", int(saleStartWarmupBefore.Seconds()))
	m.setRuntime(taskID, "waiting_start", message, "info")
	if err := m.ticketClient(proxyRuntime).WarmupShow(ctx, saleStartWarmupRequestCount); err != nil {
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
	if task.ProjectID <= 0 {
		return errors.New("请先获取票务信息并选择票信息")
	}
	taskMode := model.NormalizeTaskMode(task.TaskMode)
	durationMode := model.NormalizeDurationMode(task.DurationMode)
	hasRushStage := taskMode == model.TaskModeRush || taskMode == model.TaskModeHybrid
	hasRestockStage := taskMode == model.TaskModeRestock || taskMode == model.TaskModeHybrid
	if hasRushStage && model.NormalizeProxyMode(task.ProxyMode) == model.ProxyModeConcurrent && task.ProxyGroupID <= 0 {
		return errors.New("并发代理需要选择代理组")
	}
	if hasRestockStage && len(task.SelectedTickets) == 0 {
		return errors.New("回流蹲票模式请至少选择一个票种")
	}
	if hasRushStage && (task.ScreenID <= 0 || task.SKUID <= 0) {
		return errors.New("请先获取票务信息并选择票信息")
	}
	if hasRushStage && strings.TrimSpace(task.SaleStart) == "" {
		return errors.New("票档缺少起售时间")
	}
	if taskMode == model.TaskModeHybrid && task.RushDurationSeconds <= 0 {
		return errors.New("抢票+回流捡漏模式需要设置大于 0 的抢票持续秒数")
	}
	if hasRestockStage && durationMode == model.DurationModeLimited {
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
	return tickettime.Parse(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
