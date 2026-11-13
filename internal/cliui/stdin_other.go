//go:build !unix

package cliui

import (
	"os"
	"time"
)

// 非 Unix 平台无法可靠区分裸 ESC 和 ESC 序列，保持原样。
func fdReadable(*os.File, time.Duration) bool { return true }
