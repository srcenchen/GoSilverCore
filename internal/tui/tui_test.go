package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewDefaultsToReceive(t *testing.T) {
	m := New()
	if m.mode != modeReceive {
		t.Fatalf("默认应为接收模式, got %v", m.mode)
	}
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init 应返回命令")
	}
	if v := m.View(); !strings.Contains(v, "接收模式") {
		t.Fatalf("接收模式视图缺少标题: %q", v)
	}
}

func TestSwitchToSendAndBack(t *testing.T) {
	m := New()

	// 接收态按 s 切发送（进入文件输入）
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = model.(*Model)
	if m.mode != modeSendInputFile {
		t.Fatalf("按 s 应进入文件输入态, got %v", m.mode)
	}
	if v := m.View(); !strings.Contains(v, "发送模式") {
		t.Fatalf("发送输入视图缺少标题: %q", v)
	}

	// 不存在的文件应报错并停留
	m.input.SetValue("/no/such/file/definitely-missing.bin")
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(*Model)
	if m.mode != modeSendInputFile || m.err == nil {
		t.Fatalf("无效文件应停留并报错, mode=%v err=%v", m.mode, m.err)
	}

	// esc 返回接收模式
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(*Model)
	if m.mode != modeReceive {
		t.Fatalf("esc 应返回接收模式, got %v", m.mode)
	}
}

func TestWindowResizeSetsProgressWidth(t *testing.T) {
	m := New()
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Model)
	if m.prog.Width <= 0 {
		t.Fatalf("进度条宽度应被设置, got %d", m.prog.Width)
	}
}
