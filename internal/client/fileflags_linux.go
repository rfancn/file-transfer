//go:build linux

package client

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// openNoFollow 阻止打开最后一级为符号链接的文件，避免临时文件被预置为链接后跟随写入。
const openNoFollow = unix.O_NOFOLLOW

// fileOwnerUID 返回文件属主 UID；无法获取时 ok 为 false。
func fileOwnerUID(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}
