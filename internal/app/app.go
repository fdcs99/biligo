package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fdcs99/biligo/internal/applog"
	"github.com/fdcs99/biligo/internal/bwsrunner"
	"github.com/fdcs99/biligo/internal/config"
	"github.com/fdcs99/biligo/internal/httpapi"
	"github.com/fdcs99/biligo/internal/panelauth"
	"github.com/fdcs99/biligo/internal/runner"
	"github.com/fdcs99/biligo/internal/store"
	"github.com/fdcs99/biligo/internal/webui"
)

type Status string

const (
	StatusStopped  Status = "stopped"
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusStopping Status = "stopping"
	StatusFailed   Status = "failed"
)

type App struct {
	mu            sync.Mutex
	configPath    string
	consoleWriter io.Writer

	status        Status
	lastErr       error
	runtime       *runtimeState
	panelURL      string
	cfgPath       string
	logPath       string
	panelPassword string
}

type runtimeState struct {
	cfg        config.Config
	logFile    *os.File
	logger     *applog.Logger
	store      *store.Store
	runner     *runner.Manager
	bwsRunner  *bwsrunner.Manager
	server     *http.Server
	listener   net.Listener
	serverDone chan error
}

func New(configPath string) *App {
	return &App{
		configPath:    configPath,
		consoleWriter: os.Stdout,
		status:        StatusStopped,
	}
}

func (a *App) SetConsoleWriter(writer io.Writer) {
	if writer == nil {
		writer = io.Discard
	}
	a.mu.Lock()
	a.consoleWriter = writer
	runtime := a.runtime
	a.mu.Unlock()
	if runtime != nil && runtime.logger != nil {
		runtime.logger.SetConsoleWriter(writer)
	}
}

func (a *App) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.status == StatusRunning || a.status == StatusStarting {
		a.mu.Unlock()
		return nil
	}
	a.status = StatusStarting
	a.lastErr = nil
	a.panelPassword = ""
	consoleWriter := a.consoleWriter
	configPath := a.configPath
	a.mu.Unlock()

	runtime, err := newRuntime(ctx, configPath, consoleWriter)
	if err != nil {
		a.setFailed(err)
		return err
	}

	if err := runtime.start(); err != nil {
		runtime.close()
		a.setFailed(err)
		return err
	}

	a.mu.Lock()
	a.runtime = runtime
	a.status = StatusRunning
	a.lastErr = nil
	a.panelURL = panelURL(runtime.cfg.Server.Addr)
	a.cfgPath = runtime.cfg.Path
	a.logPath = runtime.cfg.Logging.File.Path
	a.panelPassword = runtime.cfg.Auth.Password
	a.mu.Unlock()

	runtime.logger.Infof("Biligo 服务监听 %s。", runtime.cfg.Server.Addr)
	go a.watchServer(runtime)
	return nil
}

func (a *App) Stop(ctx context.Context) error {
	a.mu.Lock()
	if a.status == StatusStopped {
		a.mu.Unlock()
		return nil
	}
	runtime := a.runtime
	a.status = StatusStopping
	a.runtime = nil
	a.mu.Unlock()

	if runtime == nil {
		a.setStopped(nil)
		return nil
	}

	err := runtime.stop(ctx)
	a.setStopped(err)
	return err
}

func (a *App) Restart(ctx context.Context) error {
	if err := a.Stop(ctx); err != nil {
		return err
	}
	return a.Start(ctx)
}

func (a *App) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *App) LastError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

func (a *App) PanelURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.panelURL
}

func (a *App) ConfigPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfgPath != "" {
		return a.cfgPath
	}
	if a.configPath != "" {
		return a.configPath
	}
	if env := strings.TrimSpace(os.Getenv("BILIGO_CONFIG")); env != "" {
		return env
	}
	return "config.yaml"
}

func (a *App) LogPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.logPath != "" {
		return a.logPath
	}
	return "logs/biligo.log"
}

func (a *App) PanelPassword() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.panelPassword
}

func (a *App) GeneratedPanelPassword() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime == nil {
		return ""
	}
	return a.runtime.cfg.GeneratedPanelPassword
}

func (a *App) Logger() *applog.Logger {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime == nil {
		return nil
	}
	return a.runtime.logger
}

func (a *App) watchServer(runtime *runtimeState) {
	err := <-runtime.serverDone
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	a.mu.Lock()
	if a.runtime == runtime {
		a.runtime = nil
		a.status = StatusFailed
		a.lastErr = err
	}
	a.mu.Unlock()
	if runtime.logger != nil {
		runtime.logger.Errorf("run server: %v", err)
	}
	runtime.close()
}

func (a *App) setFailed(err error) {
	a.mu.Lock()
	a.status = StatusFailed
	a.lastErr = err
	a.runtime = nil
	a.mu.Unlock()
}

func (a *App) setStopped(err error) {
	a.mu.Lock()
	a.status = StatusStopped
	a.lastErr = err
	a.mu.Unlock()
}

func newRuntime(ctx context.Context, configPath string, consoleWriter io.Writer) (*runtimeState, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	var logFile *os.File
	if cfg.Logging.File.Enabled {
		logFile, err = openLogFile(cfg.Logging.File.Path)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
	}
	logger := applog.NewWithOutputs(cfg.Logging.Levels, consoleWriter, cfg.Logging.Color, logFile)
	if logFile != nil {
		logger.Infof("日志文件已启用：%s", cfg.Logging.File.Path)
	}
	if cfg.GeneratedConfigFile {
		logger.Infof("配置文件已自动生成：%s", cfg.Path)
	}
	if cfg.GeneratedPanelPassword != "" {
		logger.Infof("面板登录密码已生成并写入 %s：%s", cfg.Path, cfg.GeneratedPanelPassword)
	}

	var routerOptions []httpapi.RouterOption
	if webui.Embedded() {
		webFS, err := webui.Dist()
		if err != nil {
			_ = closeFile(logFile)
			return nil, fmt.Errorf("load embedded web assets: %w", err)
		}
		routerOptions = append(routerOptions, httpapi.WithWebFS(webFS))
		logger.Infof("Web 控制台使用嵌入前端资源，同端口提供页面：%s", cfg.Server.Addr)
	} else {
		logger.Infof("Web 控制台未嵌入，仅启用 API 服务。")
	}

	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		_ = closeFile(logFile)
		return nil, fmt.Errorf("open database: %w", err)
	}

	pausedTasks, err := db.PauseInterruptedTasks(ctx)
	if err != nil {
		_ = db.Close()
		_ = closeFile(logFile)
		return nil, fmt.Errorf("pause interrupted tasks: %w", err)
	}
	if len(pausedTasks) > 0 {
		logger.Warnf("启动时自动停止 %d 个上次未结束任务。", len(pausedTasks))
	}
	pausedBWSTasks, err := db.PauseInterruptedBWSTasks(ctx)
	if err != nil {
		_ = db.Close()
		_ = closeFile(logFile)
		return nil, fmt.Errorf("pause interrupted BWS tasks: %w", err)
	}
	if len(pausedBWSTasks) > 0 {
		logger.Warnf("启动时自动停止 %d 个上次未结束 BWS 预约任务。", len(pausedBWSTasks))
	}

	httpRuntime := httpapi.NewRuntime(db, panelauth.NewManager(cfg.Auth.Password, 24*time.Hour), logger, routerOptions...)
	return &runtimeState{
		cfg:        cfg,
		logFile:    logFile,
		logger:     logger,
		store:      db,
		runner:     httpRuntime.Runner,
		bwsRunner:  httpRuntime.BWSRunner,
		server:     &http.Server{Handler: httpRuntime.Router},
		serverDone: make(chan error, 1),
	}, nil
}

func (r *runtimeState) start() error {
	listener, err := net.Listen("tcp", r.cfg.Server.Addr)
	if err != nil {
		return err
	}
	r.listener = listener
	go func() {
		r.serverDone <- r.server.Serve(listener)
	}()
	return nil
}

func (r *runtimeState) stop(ctx context.Context) error {
	if r.logger != nil {
		r.logger.Infof("正在停止 Biligo 服务。")
	}
	var result error
	if r.runner != nil {
		if err := r.runner.StopAll(ctx); err != nil {
			result = err
		}
	}
	if r.bwsRunner != nil {
		if err := r.bwsRunner.StopAll(ctx); err != nil && result == nil {
			result = err
		}
	}
	if r.server != nil {
		if err := r.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) && result == nil {
			result = err
		}
	}
	select {
	case err := <-r.serverDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && result == nil {
			result = err
		}
	default:
	}
	if err := r.close(); err != nil && result == nil {
		result = err
	}
	return result
}

func (r *runtimeState) close() error {
	var result error
	if r.store != nil {
		if err := r.store.Close(); err != nil {
			result = err
		}
		r.store = nil
	}
	if err := closeFile(r.logFile); err != nil && result == nil {
		result = err
	}
	r.logFile = nil
	return result
}

func openLogFile(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func closeFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func panelURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "http://127.0.0.1:8080/"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			return "http://127.0.0.1" + addr + "/"
		}
		return "http://" + strings.TrimRight(addr, "/") + "/"
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}
