package gosilver

import (
	"os"
	"path/filepath"
	"strings"
)

// resolveSavePath 根据保存目录与远端文件名计算最终落盘路径。
// saveDir 为空时使用可定位的默认目录；目录不存在则创建；文件名做跨平台清洗。
func resolveSavePath(saveDir, rawName string) (string, error) {
	dir := saveDir
	if dir == "" {
		dir = defaultSaveDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, SanitizeFileName(rawName)), nil
}

// defaultSaveDir 返回一个可定位的默认保存目录：可执行文件同级的 GoSilverDownloads。
// 这样即使在 Windows 下双击运行（工作目录不确定），用户也能稳定地在程序旁找到下载产物。
func defaultSaveDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "GoSilverDownloads")
	}
	return "GoSilverDownloads"
}

// SanitizeFileName 清洗远端传来的文件名：剥离路径成分、替换各平台非法字符、
// 处理 Windows 结尾点/空格与保留设备名，保证在 Windows/macOS/Linux 上都能安全创建文件。
// 导出以便其他接收路径（如 internal/receiver）复用同一套防路径穿越逻辑，避免行为不一致。
func SanitizeFileName(name string) string {
	// 替换 Windows 非法字符 \ / : * ? " < > | 与控制字符；同时消除路径分隔符以防目录穿越
	name = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, name)
	// Windows 不允许文件名以点或空格结尾
	name = strings.TrimRight(name, " .")
	if name == "" {
		return "download"
	}
	// 规避 Windows 保留设备名（CON/PRN/AUX/NUL/COM1-9/LPT1-9）
	stem := name
	if i := strings.IndexByte(name, '.'); i >= 0 {
		stem = name[:i]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	return name
}
