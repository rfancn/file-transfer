package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/server"
	"github.com/rfancn/file-transfer/internal/version"
)

func main() {
	cfgPath := flag.String("config", "", "配置文件路径")
	flag.StringVar(cfgPath, "c", "", "配置文件路径（简写）")
	showVersion := flag.Bool("version", false, "输出版本")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version)
		return
	}
	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if *cfgPath == "" {
		*cfgPath = os.Getenv("FT_SERVER_CONFIG")
		if *cfgPath == "" {
			*cfgPath = config.DefaultServerConfig()
		}
	}
	if err := logging.Init(cfg.Server.LogFile, logging.Rotate{
		MaxAgeDays: cfg.Log.MaxAgeDays,
		MaxBackups: cfg.Log.MaxBackups,
		MaxSizeMB:  cfg.Log.MaxSizeMB,
		Compress:   cfg.Log.Compress,
	}); err != nil {
		log.Fatalf("初始化日志失败: %v", err)
	}
	logging.Info("S3 配置", "endpoint", cfg.S3.Endpoint, "bucket", cfg.S3.Bucket)
	if changed, err := cfg.EnsureAuthSecrets(); err != nil {
		log.Fatalf("生成 auth 密钥失败: %v", err)
	} else if changed {
		if err := config.SaveServerConfig(*cfgPath, cfg); err != nil {
			logging.Warn("已生成随机 auth 密钥但无法写回配置，重启后现有登录会失效，请手动设置 auth.token_secret / auth.upload_secret", "file", *cfgPath, "err", err)
		} else {
			logging.Warn("检测到默认或空的 auth 密钥，已生成随机密钥并写入配置", "file", *cfgPath)
		}
	}
	rt, err := cfg.Runtime(*cfgPath)
	if err != nil {
		log.Fatalf("配置无效: %v", err)
	}
	srv, err := server.New(cfg, rt)
	if err != nil {
		logging.Fatal("启动失败", "err", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-sigCh:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = srv.Shutdown(ctx)
			cancel()
			return
		case err := <-errCh:
			if err != nil && err.Error() != "http: Server closed" {
				logging.Fatal("服务异常", "err", err)
			}
			return
		}
	}
}
