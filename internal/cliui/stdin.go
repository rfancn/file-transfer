package cliui

import (
	"bufio"
	"io"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

var escPressed atomic.Bool

// escReader 把裸 ESC 转成 Ctrl-C，让各交互组件支持按 ESC 取消；
// 方向键等 ESC 序列保持原样。
type escReader struct {
	r *bufio.Reader
	f *os.File
}

var interactiveStdin io.ReadCloser

func init() {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		interactiveStdin = &escReader{r: bufio.NewReader(os.Stdin), f: os.Stdin}
	}
}

func promptStdin() io.ReadCloser {
	if interactiveStdin != nil {
		return interactiveStdin
	}
	return nil
}

func (e *escReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b, err := e.r.ReadByte()
	if err != nil {
		return 0, err
	}
	if b != 0x1b {
		p[0] = b
		return 1, nil
	}
	// ESC 后短时间内没有后续字节，视为单独按下 ESC。
	if e.r.Buffered() == 0 && !fdReadable(e.f, 30*time.Millisecond) {
		escPressed.Store(true)
		p[0] = 0x03
		return 1, nil
	}
	p[0] = b
	return 1, nil
}

// Close 不关闭标准输入，避免影响后续交互。
func (e *escReader) Close() error { return nil }
