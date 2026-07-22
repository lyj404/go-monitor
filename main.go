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

	"github.com/lyj404/go-monitor/alerter"
	"github.com/lyj404/go-monitor/collector"
	"github.com/lyj404/go-monitor/config"
	"github.com/lyj404/go-monitor/server"
	"github.com/lyj404/go-monitor/store"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal("加载配置失败:", err)
	}

	snap := cfg.Snapshot()

	dataDir, err := resolveDataDir(*cfgPath, snap.Server.DataDir)
	if err != nil {
		log.Fatal("无法确定数据目录:", err)
	}
	log.Println("数据目录:", dataDir)

	// Always create the alerter so enabling alerts via config reload works
	// without a process restart. CheckWithConfig no-ops when alert.enabled=false.
	al := alerter.New()
	if snap.Alert.Enabled {
		log.Println("报警功能已启用")
	} else {
		log.Println("报警功能未启用（可通过配置热更新开启）")
	}

	db, err := store.NewDB(dataDir)
	if err != nil {
		log.Println("数据库初始化失败:", err)
	}

	// Wire alert history before collector starts to avoid racing SetOnAlert.
	if db != nil {
		al.SetOnAlert(func(alertType, message, currentValue, threshold string) {
			if err := db.SaveAlert(alertType, message, currentValue, threshold); err != nil {
				log.Println("保存告警历史失败:", err)
			}
		})
	}

	col := collector.NewCollector(cfg, al)
	if snap.Monitor.LanWanSplit {
		if err := collector.EnableLanWanSplit(); err != nil {
			log.Println("LAN/WAN 流量分类初始化失败（nftables 不可用）:", err)
		} else {
			log.Println("LAN/WAN 流量分类已启用")
		}
	}
	col.Start()
	defer col.Stop()
	defer collector.DisableLanWanSplit()

	svr := server.NewServer(cfg, col, db)
	handler := svr.Routes()

	addr := fmt.Sprintf(":%d", snap.Server.Port)
	log.Println("服务器启动于 http://localhost" + addr)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
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

	// Drain in-flight HTTP requests first so handlers still see live
	// collector/db references; only then stop background subsystems.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Println("服务器强制关闭:", err)
	} else {
		log.Println("HTTP 服务已关闭")
	}

	svr.Close()
	col.Stop()
	al.Close()
	if db != nil {
		db.Close()
	}
	log.Println("服务已关闭")
}

// resolveDataDir picks a stable data directory in this priority order:
//  1. server.data_dir from config (if set) — absolute, or resolved
//     relative to the executable directory
//  2. legacy fallback for backward compatibility:
//     - if --config is an absolute path → <config_dir>/data
//     (matches the Debian package layout: /etc/go-monitor/data)
//     - otherwise → <executable_dir>/data
//
// Keeping the legacy fallback means existing installs that never set
// data_dir continue to find their old monitor.db after upgrade.
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

	if filepath.IsAbs(cfgPath) {
		return filepath.Join(filepath.Dir(cfgPath), "data"), nil
	}
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(execPath), "data"), nil
}
