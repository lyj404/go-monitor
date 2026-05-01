package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go-monitor/alerter"
	"go-monitor/collector"
	"go-monitor/config"
	"go-monitor/server"
	"go-monitor/store"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal("加载配置失败:", err)
	}

	// Determine config directory for data path
	var dataDir string
	if filepath.IsAbs(*cfgPath) {
		dataDir = filepath.Dir(*cfgPath) + "/data"
	} else {
		execPath, _ := os.Executable()
		dataDir = filepath.Dir(execPath) + "/data"
	}

	_ = dataDir // suppress unused warning

	var al *alerter.Alerter
	if cfg.Alert.Enabled {
		al = alerter.New(cfg)
		log.Println("报警功能已启用")
	} else {
		log.Println("报警功能未启用")
	}

	col := collector.NewCollector(cfg, al)
	col.Start()
	defer col.Stop()

	db, err := store.NewDB(dataDir)
	if err != nil {
		log.Println("数据库初始化失败:", err)
	}

	svr := server.NewServer(cfg, col, db)
	handler := svr.Routes()

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Println("服务器启动于 http://localhost" + addr)

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start hourly tasks with shutdown context as stop signal
	if db != nil {
		db.StartHourlyTasks(ctx.Done())
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("服务器启动失败:", err)
		}
	}()

	<-ctx.Done()

	log.Println("正在关闭服务...")

	if db != nil {
		db.Close()
	}
	svr.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Println("服务器强制关闭:", err)
	} else {
		log.Println("服务已关闭")
	}
}
