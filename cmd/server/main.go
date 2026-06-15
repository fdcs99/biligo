package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fdcs99/biligo/internal/applog"
	"github.com/fdcs99/biligo/internal/config"
	"github.com/fdcs99/biligo/internal/httpapi"
	"github.com/fdcs99/biligo/internal/panelauth"
	"github.com/fdcs99/biligo/internal/store"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("load config: %v", err)
	}
	logger := applog.New(cfg.Logging.Levels, cfg.Logging.Color)
	if cfg.GeneratedConfigFile {
		logger.Infof("配置文件已自动生成：%s", cfg.Path)
	}
	if cfg.GeneratedPanelPassword != "" {
		logger.Infof("面板登录密码已生成并写入 %s：%s", cfg.Path, cfg.GeneratedPanelPassword)
	}

	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		logger.Errorf("open database: %v", err)
		os.Exit(1)
	}
	defer db.Close()

	pausedTasks, err := db.PauseInterruptedTasks(context.Background())
	if err != nil {
		logger.Errorf("pause interrupted tasks: %v", err)
		os.Exit(1)
	}
	if len(pausedTasks) > 0 {
		logger.Warnf("启动时自动停止 %d 个上次未结束任务。", len(pausedTasks))
	}

	router := httpapi.NewRouter(db, panelauth.NewManager(cfg.Auth.Password, 24*time.Hour), logger)
	logger.Infof("Biligo 服务监听 %s。", cfg.Server.Addr)
	if err := router.Run(cfg.Server.Addr); err != nil {
		logger.Errorf("run server: %v", err)
		os.Exit(1)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s [ERROR] %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	os.Exit(1)
}
