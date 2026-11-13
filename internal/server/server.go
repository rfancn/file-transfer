// Package server 负责 ft-server 的依赖装配与生命周期。
package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/rfancn/file-transfer/internal/adminuser"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/iam"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/s3"
	"github.com/rfancn/file-transfer/internal/server/app"
	"github.com/rfancn/file-transfer/internal/server/router"
)

type Server struct {
	app     *app.App
	httpSrv *http.Server
}

func New(cfg config.ServerConfig, rt config.Runtime) (*Server, error) {
	dataCreds, adminCreds, credErr := config.LoadServiceSecrets()
	ready := credErr == nil
	if !ready {
		logging.Warn("系统尚未初始化，业务接口不可用，请执行 ft-admin init")
	}
	data, err := s3.New(cfg.S3.Endpoint, dataCreds.AccessKey, dataCreds.SecretKey, cfg.S3.Region, cfg.S3.Bucket)
	if err != nil {
		return nil, fmt.Errorf("数据身份客户端: %w", err)
	}
	adminS3, err := s3.New(cfg.S3.Endpoint, adminCreds.AccessKey, adminCreds.SecretKey, cfg.S3.Region, cfg.S3.Bucket)
	if err != nil {
		return nil, fmt.Errorf("管理身份 S3 客户端: %w", err)
	}
	im, err := iam.New(cfg.S3.Endpoint, adminCreds.AccessKey, adminCreds.SecretKey, cfg.S3.Region, cfg.S3.Bucket)
	if err != nil {
		return nil, fmt.Errorf("管理身份客户端: %w", err)
	}
	im.PlaceholderS3 = data
	adminPath := adminuser.Path()
	admins, initialPassword, err := adminuser.LoadOrCreate(adminPath)
	if err != nil {
		return nil, fmt.Errorf("初始化管理员账号: %w", err)
	}
	if initialPassword != "" {
		logging.Warn("已生成 ft-server 管理员账号，请妥善保存", "username", adminuser.DefaultName, "password", initialPassword, "file", adminPath)
	}

	s := &Server{app: &app.App{
		Cfg:     cfg,
		RT:      rt,
		Data:    data,
		AdminS3: adminS3,
		IAM:     im,
		Admins:  admins,
		Ready:   ready,
	}}
	s.httpSrv = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           router.New(s.app),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	s.app.Shutdown = s.Shutdown
	return s, nil
}

func (s *Server) Start() error {
	logging.Info("ft-server 监听", "address", s.app.Cfg.Server.Listen)
	return s.httpSrv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error { return s.httpSrv.Shutdown(ctx) }
