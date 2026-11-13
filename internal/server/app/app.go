// Package app 汇总 ft-server 运行时依赖，供 handler、middleware 和 router 共享。
package app

import (
	"context"

	"github.com/rfancn/file-transfer/internal/adminuser"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/iam"
	"github.com/rfancn/file-transfer/internal/s3"
)

type App struct {
	Cfg      config.ServerConfig
	RT       config.Runtime
	Data     *s3.Manager
	AdminS3  *s3.Manager
	IAM      *iam.Manager
	Admins   *adminuser.Store
	Ready    bool
	Shutdown func(ctx context.Context) error
}

func (a *App) S3Ready() bool { return a.Ready }
