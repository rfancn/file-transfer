//go:build !linux

package client

import "os"

// openNoFollow 在非 Linux 平台没有对应语义，符号链接防护由 os.Root 边界和 Lstat 检查承担。
const openNoFollow = 0

// fileOwnerUID 在非 Linux 平台不检查属主。
func fileOwnerUID(os.FileInfo) (uint32, bool) {
	return 0, false
}
