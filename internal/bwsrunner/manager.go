package bwsrunner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fdcs99/biligo/internal/bws"
	"github.com/fdcs99/biligo/internal/events"
	"github.com/fdcs99/biligo/internal/model"
	proxynet "github.com/fdcs99/biligo/internal/proxy"
	"github.com/fdcs99/biligo/internal/store"
	"github.com/fdcs99/biligo/internal/timesync"
)

type TimeSynchronizer interface {
	Sync(ctx context.Context) (timesync.Result, error)
}

type Manager struct {
	store    *store.Store
	client   *bws.Client
	timeSync TimeSynchronizer
	hub      *events.Hub

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	wg      sync.WaitGroup
}

const (
	waitTick                  = 50 * time.Millisecond
	waitReportInterval        = time.Second
	defaultProxyAPIPullBefore = 5 * time.Minute
	proxyAPIPullMaxAttempts   = 3
)

var pullKuaidailiDPS = proxynet.PullKuaidailiDPS

func NewManager(store *store.Store, client *bws.Client, hub *events.Hub) *Manager {
	return NewManagerWithTimeSync(store, client, hub, timesync.NewClient(nil))
}

func NewManagerWithTimeSync(store *store.Store, client *bws.Client, hub *events.Hub, timeSync TimeSynchronizer) *Manager {
	if client == nil {
		client = bws.NewClient(nil)
	}
	if timeSync == nil {
		timeSync = timesync.NewClient(nil)
	}
	return &Manager{
		store:    store,
		client:   client,
		timeSync: timeSync,
		hub:      hub,
		running:  map[int64]context.CancelFunc{},
	}
}

func (m *Manager) Dispatch(ctx context.Context, taskID int64) (model.BWSTask, error) {
	task, err := m.store.GetBWSTask(ctx, taskID)
	if err != nil {
		return model.BWSTask{}, err
	}
	if err := validateTask(task); err != nil {
		task, log, setErr := m.store.SetBWSTaskRuntime(ctx, taskID, model.BWSTaskRuntimeUpdate{
			Status:      "failed",
			LastMessage: err.Error(),
		}, "error")
		if setErr == nil {
			m.publishTaskAndLog(task, log)
		}
		return model.BWSTask{}, err
	}
	_, cookie, err := m.store.GetAccountCookie(ctx, task.AccountID)
	if err != nil {
		return model.BWSTask{}, err
	}
	if strings.TrimSpace(cookie) == "" {
		return model.BWSTask{}, errors.New("账号未保存 Cookie")
	}

	task, err = m.syncTaskTime(ctx, task)
	if err != nil {
		return model.BWSTask{}, err
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
	message := "BWS 预约任务已下发，等待预约开始时间。"
	if model.NormalizeBWSTaskMode(task.Mode) == model.BWSTaskModeImmediate {
		status = "running"
		message = "BWS 预约任务已下发，立即开始预约。"
	}
	task, log, err := m.store.SetBWSTaskRuntime(ctx, taskID, model.BWSTaskRuntimeUpdate{
		Status:      status,
		LastMessage: message,
	}, "info")
	if err != nil {
		m.remove(taskID)
		cancel()
		return model.BWSTask{}, err
	}
	m.publishTaskAndLog(task, log)

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(runCtx, taskID, cookie)
	}()
	return task, nil
}

func (m *Manager) Pause(ctx context.Context, taskID int64) (model.BWSTask, error) {
	m.mu.Lock()
	cancel := m.running[taskID]
	if cancel != nil {
		cancel()
		delete(m.running, taskID)
	}
	m.mu.Unlock()

	task, log, err := m.store.SetBWSTaskRuntime(ctx, taskID, model.BWSTaskRuntimeUpdate{
		Status:      "paused",
		LastMessage: "BWS 预约任务已停止。",
	}, "warn")
	if err != nil {
		return model.BWSTask{}, err
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

	tasks, logs, err := m.store.PauseActiveBWSTasks(ctx, "服务停止，BWS 预约任务已自动停止。")
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

	task, err := m.store.GetBWSTask(context.Background(), taskID)
	if err != nil {
		return
	}
	offset := time.Duration(task.TimeOffsetMillis) * time.Millisecond
	proxyRuntime, err := m.newBWSProxyRuntime(context.Background(), taskID, task)
	if err != nil {
		m.setRuntime(taskID, "failed", "BWS 代理组准备失败："+err.Error(), "error")
		return
	}
	if model.NormalizeBWSTaskMode(task.Mode) != model.BWSTaskModeImmediate {
		target := time.Unix(task.ReserveBeginTime, 0)
		if !m.waitUntil(ctx, taskID, target, offset, proxyRuntime) {
			return
		}
	}
	if proxyRuntime != nil {
		if err := proxyRuntime.ensureReady(ctx); err != nil {
			m.setRuntime(taskID, "failed", "BWS 代理组准备失败："+err.Error(), "error")
			return
		}
	}
	if model.NormalizeBWSTaskMode(task.Mode) == model.BWSTaskModeImmediate {
		m.setRuntime(taskID, "running", "立即开始执行 BWS 预约。", "info")
	} else {
		m.setRuntime(taskID, "running", "已到预约开始时间，开始执行 BWS 预约。", "info")
	}
	if isConcurrentBWSProxyTask(task) {
		m.runConcurrentReservationLoop(ctx, cookie, task, offset, proxyRuntime)
		return
	}
	m.runReservationLoop(ctx, cookie, task, offset, proxyRuntime)
}

func (m *Manager) runReservationLoop(ctx context.Context, cookie string, task model.BWSTask, offset time.Duration, proxyRuntime *bwsProxyRuntime) {
	interval := time.Duration(task.PollIntervalMillis) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		result, err := m.bwsClient(proxyRuntime).Reserve(ctx, cookie, bws.ReserveInput{
			TicketNo:        task.TicketNo,
			ReserveID:       task.ReserveID,
			Year:            task.Year,
			TimestampMillis: time.Now().Add(offset).UnixMilli(),
		})
		if ctx.Err() != nil {
			return
		}
		if shouldSwitchBWSProxy(proxyRuntime, result, err) {
			reason := bwsProxyFailureReason(result, err)
			if switchErr := proxyRuntime.switchNext(ctx, reason); switchErr != nil {
				m.setRuntime(task.ID, "failed", "BWS 代理切换失败："+switchErr.Error()+"。", "error")
				return
			}
			m.setRuntime(task.ID, "running", fmt.Sprintf("BWS 预约触发代理切换：%s，已切换到 %s。", reason, proxyNodeLabel(proxyRuntime.currentNode())), "warn")
			continue
		}
		if err != nil && result.Code == -1 {
			m.setRuntime(task.ID, "running", "网络请求失败："+err.Error()+"，继续重试。", "warn")
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		message := bws.CodeMessage(result.Code, result.Message)
		switch {
		case result.Code == 0:
			m.setRuntime(task.ID, "success", fmt.Sprintf("BWS 预约成功：%s。", task.Title), "info")
			return
		case bws.IsTerminalCode(result.Code):
			m.setRuntime(task.ID, "failed", fmt.Sprintf("[%d] %s，任务停止。", result.Code, message), "error")
			return
		case bws.IsRetryCode(result.Code):
			m.setRuntime(task.ID, "running", fmt.Sprintf("[%d] %s，继续预约。", result.Code, message), "warn")
			if !wait(ctx, retryDelay(result.Code, interval)) {
				return
			}
		default:
			m.setRuntime(task.ID, "running", fmt.Sprintf("[%d] %s，继续预约。", result.Code, message), "warn")
			if !wait(ctx, interval) {
				return
			}
		}
	}
}

type bwsProxyWorkerResult struct {
	kind    string
	node    model.ProxyNode
	code    int
	message string
}

const (
	bwsProxyWorkerSuccess       = "success"
	bwsProxyWorkerGlobalFailure = "global_failure"
	bwsProxyWorkerNodeFailure   = "node_failure"
)

func (m *Manager) runConcurrentReservationLoop(ctx context.Context, cookie string, task model.BWSTask, offset time.Duration, proxyRuntime *bwsProxyRuntime) {
	if proxyRuntime == nil || len(proxyRuntime.nodes) == 0 {
		m.setRuntime(task.ID, "failed", "BWS 并发代理需要可用代理节点。", "error")
		return
	}
	interval := time.Duration(task.PollIntervalMillis) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan bwsProxyWorkerResult, len(proxyRuntime.nodes))
	var wg sync.WaitGroup
	started := 0
	for _, node := range proxyRuntime.nodes {
		httpClient, err := proxynet.NewHTTPClient(node)
		if err != nil {
			_, _ = m.store.SetProxyNodeTestResult(context.Background(), node.ID, "error", err.Error(), 0, "")
			results <- bwsProxyWorkerResult{kind: bwsProxyWorkerNodeFailure, node: node, message: err.Error()}
			continue
		}
		started++
		workerClient := m.client.WithHTTPClient(httpClient)
		wg.Add(1)
		go func(node model.ProxyNode, client *bws.Client) {
			defer wg.Done()
			m.runConcurrentProxyWorker(workerCtx, cookie, task, offset, interval, node, client, results)
		}(node, workerClient)
	}
	if started == 0 {
		close(results)
	} else {
		go func() {
			wg.Wait()
			close(results)
		}()
	}

	stoppedNodes := 0
	for result := range results {
		if ctx.Err() != nil {
			return
		}
		switch result.kind {
		case bwsProxyWorkerSuccess:
			cancel()
			m.setRuntime(task.ID, "success", fmt.Sprintf("BWS 预约成功：%s。", task.Title), "info")
			return
		case bwsProxyWorkerGlobalFailure:
			cancel()
			m.setRuntime(task.ID, "failed", fmt.Sprintf("[%d] %s，任务停止。", result.code, result.message), "error")
			return
		case bwsProxyWorkerNodeFailure:
			stoppedNodes++
			m.setRuntime(task.ID, "running", fmt.Sprintf("BWS 并发代理节点 %s 已停止：%s。", proxyNodeLabel(result.node), result.message), "warn")
			if stoppedNodes >= len(proxyRuntime.nodes) {
				cancel()
				m.setRuntime(task.ID, "failed", "代理组无可用节点：所有并发代理线程已停止。", "error")
				return
			}
		}
	}
	if ctx.Err() == nil {
		m.setRuntime(task.ID, "failed", "代理组无可用节点：所有并发代理线程已停止。", "error")
	}
}

func (m *Manager) runConcurrentProxyWorker(ctx context.Context, cookie string, task model.BWSTask, offset time.Duration, interval time.Duration, node model.ProxyNode, client *bws.Client, results chan<- bwsProxyWorkerResult) {
	for {
		if ctx.Err() != nil {
			return
		}
		result, err := client.Reserve(ctx, cookie, bws.ReserveInput{
			TicketNo:        task.TicketNo,
			ReserveID:       task.ReserveID,
			Year:            task.Year,
			TimestampMillis: time.Now().Add(offset).UnixMilli(),
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil && proxynet.IsRequestError(err) {
			message := err.Error()
			_, _ = m.store.SetProxyNodeTestResult(context.Background(), node.ID, "error", message, 0, "")
			results <- bwsProxyWorkerResult{kind: bwsProxyWorkerNodeFailure, node: node, message: message}
			return
		}
		if result.Code == 412 {
			message := bws.CodeMessage(result.Code, result.Message)
			_, _ = m.store.SetProxyNodeTestResult(context.Background(), node.ID, "error", message, 0, "")
			results <- bwsProxyWorkerResult{kind: bwsProxyWorkerNodeFailure, node: node, code: result.Code, message: message}
			return
		}
		if err != nil && result.Code == -1 {
			m.setRuntime(task.ID, "running", fmt.Sprintf("BWS 并发代理节点 %s 网络请求失败：%s，继续重试。", proxyNodeLabel(node), err.Error()), "warn")
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		message := bws.CodeMessage(result.Code, result.Message)
		switch {
		case result.Code == 0:
			results <- bwsProxyWorkerResult{kind: bwsProxyWorkerSuccess, node: node, message: message}
			return
		case isBWSGlobalTerminalCode(result.Code):
			results <- bwsProxyWorkerResult{kind: bwsProxyWorkerGlobalFailure, node: node, code: result.Code, message: message}
			return
		case bws.IsRetryCode(result.Code):
			m.setRuntime(task.ID, "running", fmt.Sprintf("BWS 并发代理节点 %s：[%d] %s，继续预约。", proxyNodeLabel(node), result.Code, message), "warn")
			if !wait(ctx, retryDelay(result.Code, interval)) {
				return
			}
		default:
			m.setRuntime(task.ID, "running", fmt.Sprintf("BWS 并发代理节点 %s：[%d] %s，继续预约。", proxyNodeLabel(node), result.Code, message), "warn")
			if !wait(ctx, interval) {
				return
			}
		}
	}
}

func isBWSGlobalTerminalCode(code int) bool {
	switch code {
	case 75574, 76674, 76651:
		return true
	default:
		return false
	}
}

func (m *Manager) waitUntil(ctx context.Context, taskID int64, target time.Time, offset time.Duration, proxyRuntime *bwsProxyRuntime) bool {
	var lastReport time.Time
	for {
		if ctx.Err() != nil {
			return false
		}
		now := time.Now().Add(offset)
		remaining := target.Sub(now)
		if proxyRuntime != nil && proxyRuntime.shouldPull(remaining) {
			if err := proxyRuntime.pullAndActivate(ctx); err != nil {
				m.setRuntime(taskID, "waiting_start", "BWS 代理 API 提前拉取失败："+err.Error(), "warn")
			} else {
				m.setRuntime(taskID, "waiting_start", proxyAPIReadyMessage(proxyRuntime.nodes), "info")
			}
		}
		if !now.Before(target) {
			return true
		}
		if lastReport.IsZero() || time.Since(lastReport) >= waitReportInterval {
			m.setRuntime(taskID, "waiting_start", "等待 BWS 预约开始中，距离预约开始还有 "+formatRemaining(remaining)+"。", "info")
			lastReport = time.Now()
		}
		sleep := waitTick
		if remaining < sleep {
			sleep = remaining
		}
		if !wait(ctx, sleep) {
			return false
		}
	}
}

type bwsProxyRuntime struct {
	manager    *Manager
	taskID     int64
	group      model.ProxyGroup
	nodes      []model.ProxyNode
	index      int
	client     *bws.Client
	pulled     bool
	triedAt    time.Time
	concurrent bool
}

func isConcurrentBWSProxyTask(task model.BWSTask) bool {
	return task.ProxyGroupID > 0 && model.NormalizeProxyMode(task.ProxyMode) == model.ProxyModeConcurrent
}

func (m *Manager) newBWSProxyRuntime(ctx context.Context, taskID int64, task model.BWSTask) (*bwsProxyRuntime, error) {
	if task.ProxyGroupID <= 0 {
		return nil, nil
	}
	group, err := m.store.GetProxyGroup(ctx, task.ProxyGroupID)
	if err != nil {
		return nil, err
	}
	runtime := &bwsProxyRuntime{
		manager:    m,
		taskID:     taskID,
		group:      group,
		index:      -1,
		concurrent: isConcurrentBWSProxyTask(task),
	}
	if group.Type == model.ProxyGroupTypeAPI {
		m.setRuntime(taskID, "waiting_start", "已启用 API 代理组："+group.Name+"，将按预约开始时间提前拉取。", "info")
		return runtime, nil
	}
	if err := runtime.reloadNodes(ctx); err != nil {
		return nil, err
	}
	if !runtime.concurrent {
		if err := runtime.activateIndex(0); err != nil {
			return nil, err
		}
	}
	m.setRuntime(taskID, "waiting_start", "已启用代理组："+group.Name+"。", "info")
	return runtime, nil
}

func (p *bwsProxyRuntime) shouldPull(remaining time.Duration) bool {
	if p == nil || p.group.Type != model.ProxyGroupTypeAPI || p.pulled || remaining > p.pullBefore() {
		return false
	}
	return p.triedAt.IsZero() || time.Since(p.triedAt) >= 10*time.Second
}

func (p *bwsProxyRuntime) pullBefore() time.Duration {
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

func (p *bwsProxyRuntime) pullTimes() int {
	if p == nil || p.group.APIConfig == nil {
		return model.DefaultProxyAPIPullTimes
	}
	return model.NormalizeProxyAPIPullTimes(p.group.APIConfig["pullTimes"])
}

func (p *bwsProxyRuntime) pullAndActivate(ctx context.Context) error {
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
	nodes, err := p.pullAPIProxyNodes(ctx)
	if err != nil {
		_, _ = p.manager.store.SetProxyGroupPullResult(context.Background(), p.group.ID, "error", err.Error())
		return err
	}
	if _, err := p.manager.store.ReplaceAPIProxyNodes(context.Background(), p.group.ID, nodes); err != nil {
		return err
	}
	message := fmt.Sprintf("已拉取 %d 个代理节点。", len(nodes))
	if pullTimes := p.pullTimes(); pullTimes > 1 {
		message = fmt.Sprintf("已完成 %d 次提取，合并 %d 个代理节点。", pullTimes, len(nodes))
	}
	group, err := p.manager.store.SetProxyGroupPullResult(context.Background(), p.group.ID, "success", message)
	if err == nil {
		p.group = group
	}
	p.pulled = true
	if err := p.reloadNodes(ctx); err != nil {
		return err
	}
	if p.concurrent {
		return nil
	}
	if err := p.activateIndex(0); err != nil {
		return err
	}
	return nil
}

func (p *bwsProxyRuntime) pullAPIProxyNodes(ctx context.Context) ([]model.ProxyNodeInput, error) {
	pullTimes := p.pullTimes()
	nodes := make([]model.ProxyNodeInput, 0)
	seen := make(map[string]struct{})
	var lastErr error

	for success := 1; success <= pullTimes; success++ {
		pulled := false
		for attempt := 1; attempt <= proxyAPIPullMaxAttempts; attempt++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			pullCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			pulledNodes, err := pullKuaidailiDPS(pullCtx, p.group)
			cancel()
			if err != nil {
				lastErr = err
				p.manager.setRuntime(p.taskID, "waiting_start", fmt.Sprintf("BWS 代理 API 第 %d/%d 次提取失败（尝试 %d/%d）：%s", success, pullTimes, attempt, proxyAPIPullMaxAttempts, err.Error()), "warn")
				continue
			}
			before := len(nodes)
			for _, node := range pulledNodes {
				key := proxyNodeInputDedupKey(node)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				nodes = append(nodes, node)
			}
			if pullTimes > 1 {
				p.manager.setRuntime(p.taskID, "waiting_start", fmt.Sprintf("BWS 代理 API 第 %d/%d 次提取成功，新增 %d 个代理节点。", success, pullTimes, len(nodes)-before), "info")
			}
			pulled = true
			break
		}
		if !pulled {
			if lastErr != nil {
				return nil, fmt.Errorf("BWS 代理 API 第 %d/%d 次提取失败，已尝试 %d 次：%w", success, pullTimes, proxyAPIPullMaxAttempts, lastErr)
			}
			return nil, fmt.Errorf("BWS 代理 API 第 %d/%d 次提取失败，已尝试 %d 次", success, pullTimes, proxyAPIPullMaxAttempts)
		}
	}
	if len(nodes) == 0 {
		return nil, errors.New("代理 API 提取成功但未返回代理节点")
	}
	return nodes, nil
}

func (p *bwsProxyRuntime) ensureReady(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.group.Type == model.ProxyGroupTypeAPI && !p.pulled {
		if err := p.pullAndActivate(ctx); err != nil {
			return err
		}
		p.manager.setRuntime(p.taskID, "waiting_start", proxyAPIReadyMessage(p.nodes), "info")
		return nil
	}
	if len(p.nodes) == 0 {
		if err := p.reloadNodes(ctx); err != nil {
			return err
		}
	}
	if p.concurrent {
		return nil
	}
	if p.client != nil || len(p.nodes) == 0 {
		return nil
	}
	return p.activateIndex(0)
}

func (p *bwsProxyRuntime) reloadNodes(ctx context.Context) error {
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

func (p *bwsProxyRuntime) activateIndex(index int) error {
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
	httpClient, err := proxynet.NewHTTPClient(p.nodes[index])
	if err != nil {
		return err
	}
	p.index = index
	p.client = p.manager.client.WithHTTPClient(httpClient)
	return nil
}

func (p *bwsProxyRuntime) currentNode() model.ProxyNode {
	if p == nil || p.index < 0 || p.index >= len(p.nodes) {
		return model.ProxyNode{}
	}
	return p.nodes[p.index]
}

func (p *bwsProxyRuntime) switchNext(ctx context.Context, reason string) error {
	if p == nil {
		return errors.New("任务未启用代理组")
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

func (m *Manager) bwsClient(proxyRuntime *bwsProxyRuntime) *bws.Client {
	if proxyRuntime != nil && proxyRuntime.client != nil {
		return proxyRuntime.client
	}
	return m.client
}

func shouldSwitchBWSProxy(proxyRuntime *bwsProxyRuntime, result bws.ReserveResult, err error) bool {
	if proxyRuntime == nil {
		return false
	}
	if result.Code == 412 {
		return true
	}
	return err != nil && proxynet.IsRequestError(err)
}

func bwsProxyFailureReason(result bws.ReserveResult, err error) string {
	if err != nil {
		return err.Error()
	}
	if result.Code != 0 {
		return fmt.Sprintf("[%d] %s", result.Code, bws.CodeMessage(result.Code, result.Message))
	}
	return "代理请求失败"
}

func proxyNodeInputDedupKey(node model.ProxyNodeInput) string {
	return strings.Join([]string{
		model.NormalizeProxyProtocol(node.Protocol),
		strings.ToLower(strings.TrimSpace(node.Host)),
		strconv.Itoa(node.Port),
		strings.TrimSpace(node.Username),
		strings.TrimSpace(node.Password),
	}, "\x00")
}

func proxyNodeLabel(node model.ProxyNode) string {
	name := strings.TrimSpace(node.Name)
	address := strings.TrimSpace(node.Host)
	if node.Port > 0 {
		address = fmt.Sprintf("%s:%d", address, node.Port)
	}
	if name != "" && address != "" {
		return name + "（" + address + "）"
	}
	if name != "" {
		return name
	}
	if address != "" {
		return address
	}
	return "代理节点"
}

func proxyAPIReadyMessage(nodes []model.ProxyNode) string {
	if len(nodes) == 0 {
		return "代理 API 已拉取完成。"
	}
	return fmt.Sprintf("代理 API 已拉取完成，当前可用节点 %d 个，首个节点：%s。", len(nodes), proxyNodeLabel(nodes[0]))
}

func (m *Manager) syncTaskTime(ctx context.Context, task model.BWSTask) (model.BWSTask, error) {
	if model.NormalizeTimeSyncStrategy(task.TimeSyncStrategy) == model.TimeSyncStrategyLocal {
		updated, log, err := m.store.SetBWSTaskTimeSync(ctx, task.ID, model.TimeSyncStrategyLocal, 0, "", "BWS 预约使用本地时间。")
		if err != nil {
			return model.BWSTask{}, err
		}
		m.publishTaskAndLog(updated, log)
		return updated, nil
	}
	result, err := m.timeSync.Sync(ctx)
	if err != nil {
		updated, log, setErr := m.store.SetBWSTaskRuntime(ctx, task.ID, model.BWSTaskRuntimeUpdate{
			Status:      "failed",
			LastMessage: "BWS 时间同步失败：" + err.Error(),
		}, "error")
		if setErr == nil {
			m.publishTaskAndLog(updated, log)
		}
		return model.BWSTask{}, err
	}
	message := fmt.Sprintf("BWS 时间同步完成，偏移 %dms，平均 RTT %dms。", result.OffsetMillis, result.AverageRTTMillis)
	updated, log, err := m.store.SetBWSTaskTimeSync(ctx, task.ID, model.TimeSyncStrategyBilibili, result.OffsetMillis, result.SyncedAt.Format(time.RFC3339), message)
	if err != nil {
		return model.BWSTask{}, err
	}
	m.publishTaskAndLog(updated, log)
	return updated, nil
}

func validateTask(task model.BWSTask) error {
	if task.AccountID <= 0 {
		return errors.New("BWS 预约任务需要选择账号")
	}
	if strings.TrimSpace(task.Year) == "" {
		return errors.New("BWS 预约任务需要设置年份")
	}
	if strings.TrimSpace(task.TicketNo) == "" {
		return errors.New("BWS 预约任务需要票号")
	}
	if task.ReserveID <= 0 {
		return errors.New("BWS 预约任务需要预约 ID")
	}
	if strings.TrimSpace(task.Title) == "" {
		return errors.New("BWS 预约任务需要标题")
	}
	if model.NormalizeBWSTaskMode(task.Mode) == model.BWSTaskModeScheduled && task.ReserveBeginTime <= 0 {
		return errors.New("BWS 预约任务需要预约开始时间")
	}
	return nil
}

func retryDelay(code int, interval time.Duration) time.Duration {
	switch code {
	case 429:
		if interval < 500*time.Millisecond {
			return 500 * time.Millisecond
		}
	case 76650:
		if interval < 100*time.Millisecond {
			return 100 * time.Millisecond
		}
	}
	return interval
}

func wait(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *Manager) setRuntime(taskID int64, status string, message string, level string) {
	task, log, err := m.store.SetBWSTaskRuntime(context.Background(), taskID, model.BWSTaskRuntimeUpdate{
		Status:      status,
		LastMessage: message,
	}, level)
	if err != nil {
		return
	}
	m.publishTaskAndLog(task, log)
}

func (m *Manager) publishTaskAndLog(task model.BWSTask, log model.BWSTaskLog) {
	m.publishTask(task)
	if log.ID > 0 {
		m.publishLog(log)
	}
}

func (m *Manager) publishTask(task model.BWSTask) {
	if m.hub != nil {
		m.hub.Publish("bws.task.updated", task)
	}
}

func (m *Manager) publishLog(log model.BWSTaskLog) {
	if m.hub != nil {
		m.hub.Publish("bws.log.created", log)
	}
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
