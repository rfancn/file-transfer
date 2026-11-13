package handler

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxPathBytes    = 1024
	maxSegmentBytes = 255
)

func validateRelPath(raw, allowedDir string, download bool) (string, error) {
	if raw == "" {
		return "", errors.New("路径不能为空")
	}
	if !utf8.ValidString(raw) {
		return "", errors.New("路径不是合法 UTF-8")
	}
	if len(raw) > maxPathBytes {
		return "", errors.New("路径过长")
	}
	if strings.HasPrefix(raw, "/") {
		return "", errors.New("不允许绝对路径")
	}
	if download && strings.HasSuffix(raw, "/") {
		return "", errors.New("下载路径必须是具体文件")
	}
	if !strings.HasPrefix(raw, allowedDir+"/") {
		return "", errors.New("路径必须以 " + allowedDir + "/ 开头")
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", errors.New("路径不能包含控制字符")
		}
	}
	segs := strings.Split(raw, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", errors.New("路径包含非法段")
		}
		if len(s) > maxSegmentBytes {
			return "", errors.New("路径段过长")
		}
	}
	return raw, nil
}

func validateDownloadPath(raw string) (string, error) {
	for _, dir := range []string{"inbound", "upload"} {
		if strings.HasPrefix(raw, dir+"/") {
			return validateRelPath(raw, dir, true)
		}
	}
	return "", errors.New("下载只允许 inbound/ 或 upload/")
}

func validateUploadPath(raw string) (string, error) {
	return validateRelPath(raw, "outbound", false)
}
