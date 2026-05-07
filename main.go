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

	dataDir, err := resolveDataDir(*cfgPath, cfg.Server.DataDir)
	if err != nil {
		log.Fatal("无法确定数据目录:", err)
	}
	log.Println("数据目录:", dataDir)

	var al *alerter.Alerter
	if cfg.Alert.Enabled {
		al = alerter.New()
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if db != nil {
		db.StartHourlyTasks(ctx.Done(), cfg)
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

// resolveDataDir picks a stable data directory in this priority order:
//  1. config's server.data_dir (if set) — absolute or resolved relative to
//     the executable directory
//  2. <executable_dir>/data
//
// We deliberately do not derive it from the config file's path, since the
// data directory should not silently move when the user passes a different
// --config flag.
func resolveDataDir(cfgPath, configured string) (string, error) {
	if configured != "" {
		if filepath.IsAbs(configured) {
			return configured, nil
		}
		execPath, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.Join(filepath.Dir(execPath), configured), nil
	}
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(execPath), "data"), nil
}
