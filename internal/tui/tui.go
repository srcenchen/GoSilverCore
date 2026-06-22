// Package tui 是 GoSilver 的终端交互壳。
//
// 它完全构建在 pkg/gosilver 与 pkg/discovery 之上，不直接触碰底层 GSP 协议，
// 因此对外接口（GSP 线协议、gosilver 库 API）保持不变。
//
// 默认进入「接收模式」：监听局域网多播，收到主控端推送的分发通告后自动开始下载。
// 按 s 可切换到「发送模式」：选择文件后向局域网广播通告，空闲的接收端会自动来下载。
package tui

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go-silver-core/pkg/discovery"
	"go-silver-core/pkg/gosilver"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// senderGSPPort 是发送端 GSP 监听端口，接收端按通告中的此端口回连。
const senderGSPPort = 48080

type mode int

const (
	modeReceive mode = iota // 默认：接收/监听
	modeSendInputFile       // 发送：输入文件路径
	modeSendInputDir        // 发送：输入接收端保存目录（可空）
	modeSendServing         // 发送：正在广播并分发
)

// ---- 消息类型 ----

type announceMsg struct {
	info discovery.AnnounceInfo
	src  net.Addr
}
type progressMsg gosilver.ProgressInfo
type progressClosedMsg struct{}
type tickMsg time.Time
type errMsg struct{ err error }

// ---- 样式 ----

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("87"))
	hintStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

// Model 是 TUI 的根模型。
type Model struct {
	mode  mode
	width int

	// 接收端状态
	listenStop chan struct{}              // 关闭以停止多播监听
	announceCh chan announceMsg           // 监听协程 -> Update
	client     *gosilver.Client           // 当前下载客户端
	progressCh <-chan gosilver.ProgressInfo
	rcvFile    string                     // 当前正在下载的文件名
	rcvSender  string                     // 数据源地址
	rcvInfo    gosilver.ProgressInfo      // 最近一次进度
	lastToken  string                     // 已处理过的通告 token，用于去重

	// 发送端状态
	input        textinput.Model
	filePath     string
	saveDir      string
	server       *gosilver.Server
	announceStop chan struct{}
	peers        []gosilver.PeerProgress
	servingName  string
	servingSize  int64

	prog   progress.Model
	status string // 顶部一行状态/错误提示
	err    error
}

// New 创建一个默认进入接收模式的 TUI 模型。
func New() *Model {
	ti := textinput.New()
	ti.CharLimit = 4096
	ti.Width = 50
	return &Model{
		mode:       modeReceive,
		input:      ti,
		prog:       progress.New(progress.WithDefaultGradient()),
		announceCh: make(chan announceMsg, 8),
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.startReceiving(), waitAnnounce(m.announceCh), tick())
}

// ---- 命令 ----

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func waitAnnounce(ch <-chan announceMsg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func waitProgress(ch <-chan gosilver.ProgressInfo) tea.Cmd {
	return func() tea.Msg {
		info, ok := <-ch
		if !ok {
			return progressClosedMsg{}
		}
		return progressMsg(info)
	}
}

// startReceiving 启动多播监听，收到通告写入 announceCh。
func (m *Model) startReceiving() tea.Cmd {
	return func() tea.Msg {
		m.listenStop = make(chan struct{})
		err := discovery.Listen(m.listenStop, func(info discovery.AnnounceInfo, src net.Addr) {
			select {
			case m.announceCh <- announceMsg{info: info, src: src}:
			default: // 通道满则丢弃，避免阻塞监听协程
			}
		})
		if err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// ---- Update ----

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.prog.Width = max(20, msg.Width-20)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case announceMsg:
		cmd := m.handleAnnounce(msg)
		return m, tea.Batch(cmd, waitAnnounce(m.announceCh))

	case progressMsg:
		m.rcvInfo = gosilver.ProgressInfo(msg)
		if msg.Error != nil {
			m.err = msg.Error
		}
		return m, waitProgress(m.progressCh)

	case progressClosedMsg:
		// 下载协程结束，回到空闲监听
		return m, nil

	case tickMsg:
		if m.mode == modeSendServing && m.server != nil {
			m.peers = m.server.Snapshot()
		}
		return m, tick()

	case errMsg:
		m.err = msg.err
		return m, nil
	}

	// 文本输入态把消息转交给输入框
	if m.mode == modeSendInputFile || m.mode == modeSendInputDir {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeReceive:
		switch msg.String() {
		case "q", "ctrl+c":
			m.cleanup()
			return m, tea.Quit
		case "s":
			// 切换到发送模式：先停掉接收监听
			m.stopReceiving()
			m.mode = modeSendInputFile
			m.err = nil
			m.input.Reset()
			m.input.Placeholder = "要分发的文件路径，例如 ./contest.zip"
			m.input.Focus()
			return m, textinput.Blink
		}
		return m, nil

	case modeSendInputFile:
		switch msg.String() {
		case "ctrl+c":
			m.cleanup()
			return m, tea.Quit
		case "esc":
			return m.backToReceive()
		case "enter":
			path := m.input.Value()
			if fi, err := os.Stat(path); err != nil || fi.IsDir() {
				m.err = fmt.Errorf("文件不存在或不是普通文件: %s", path)
				return m, nil
			}
			m.filePath = path
			m.err = nil
			m.mode = modeSendInputDir
			m.input.Reset()
			m.input.Placeholder = "接收端保存目录（可留空，回车跳过）"
			m.input.Focus()
			return m, textinput.Blink
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case modeSendInputDir:
		switch msg.String() {
		case "ctrl+c":
			m.cleanup()
			return m, tea.Quit
		case "esc":
			return m.backToReceive()
		case "enter":
			m.saveDir = m.input.Value()
			m.input.Blur()
			return m.startServing()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case modeSendServing:
		switch msg.String() {
		case "q", "ctrl+c":
			m.cleanup()
			return m, tea.Quit
		case "r":
			return m.backToReceive()
		}
		return m, nil
	}
	return m, nil
}

// handleAnnounce 收到主控端推送：若处于接收态且空闲，则自动开始下载。
func (m *Model) handleAnnounce(msg announceMsg) tea.Cmd {
	if m.mode != modeReceive {
		return nil
	}
	if m.client != nil {
		return nil // 已在下载，忽略
	}
	if msg.info.Token != "" && msg.info.Token == m.lastToken {
		return nil // 同一会话已处理过
	}
	m.lastToken = msg.info.Token
	senderAddr := msg.info.SenderAddr(msg.src)

	m.client = gosilver.NewClient(senderAddr, msg.info.SaveDir)
	ch, err := m.client.StartDownload()
	if err != nil {
		m.err = err
		m.client = nil
		return nil
	}
	m.progressCh = ch
	m.rcvFile = msg.info.FileName
	m.rcvSender = senderAddr
	m.err = nil
	return waitProgress(m.progressCh)
}

func (m *Model) startServing() (tea.Model, tea.Cmd) {
	srv := gosilver.NewServer(":"+strconv.Itoa(senderGSPPort), m.filePath)
	if err := srv.Start(); err != nil {
		m.err = fmt.Errorf("启动发送服务失败: %w", err)
		m.mode = modeSendInputFile
		m.input.Focus()
		return m, textinput.Blink
	}
	m.server = srv

	name := filepath.Base(m.filePath)
	var size int64
	if fi, err := os.Stat(m.filePath); err == nil {
		size = fi.Size()
	}
	m.servingName = name
	m.servingSize = size

	m.announceStop = make(chan struct{})
	token := strconv.FormatInt(time.Now().UnixNano(), 36)
	_ = discovery.Announce(m.announceStop, discovery.AnnounceInfo{
		Port:     senderGSPPort,
		FileName: name,
		FileSize: size,
		SaveDir:  m.saveDir,
		Token:    token,
	}, 2*time.Second)

	m.mode = modeSendServing
	m.err = nil
	return m, tick()
}

func (m *Model) backToReceive() (tea.Model, tea.Cmd) {
	m.stopServing()
	m.mode = modeReceive
	m.err = nil
	return m, tea.Batch(m.startReceiving(), tick())
}

// ---- 资源生命周期 ----

func (m *Model) stopReceiving() {
	if m.listenStop != nil {
		close(m.listenStop)
		m.listenStop = nil
	}
	if m.client != nil {
		m.client.CancelDownload()
		m.client = nil
	}
	m.progressCh = nil
}

func (m *Model) stopServing() {
	if m.announceStop != nil {
		close(m.announceStop)
		m.announceStop = nil
	}
	if m.server != nil {
		m.server.Stop()
		m.server = nil
	}
	m.peers = nil
}

func (m *Model) cleanup() {
	m.stopReceiving()
	m.stopServing()
}

// ---- View ----

func (m *Model) View() string {
	switch m.mode {
	case modeReceive:
		return m.viewReceive()
	case modeSendInputFile, modeSendInputDir:
		return m.viewInput()
	case modeSendServing:
		return m.viewServing()
	}
	return ""
}

func (m *Model) viewReceive() string {
	b := titleStyle.Render("GoSilver  · 接收模式") + "\n"
	b += hintStyle.Render("监听局域网推送中…") + "\n\n"

	if m.client != nil {
		pct := m.rcvInfo.Percentage / 100
		b += labelStyle.Render("文件: ") + m.rcvFile + "\n"
		b += labelStyle.Render("来源: ") + m.rcvSender + "\n"
		b += m.prog.ViewAs(pct) + "\n"
		b += fmt.Sprintf("%s  %d/%d 块  %d Mbps  [%s]\n",
			fmt.Sprintf("%.1f%%", m.rcvInfo.Percentage),
			m.rcvInfo.Downloaded, m.rcvInfo.TotalChunks, m.rcvInfo.SpeedMbps, m.rcvInfo.Status)
		if m.rcvInfo.Status == "completed" {
			b += okStyle.Render("🎉 下载完成") + "\n"
		}
	} else {
		b += hintStyle.Render("等待主控端推送下载任务…") + "\n"
	}

	b += m.errLine()
	b += "\n" + hintStyle.Render("[s] 切换发送模式   [q] 退出")
	return b
}

func (m *Model) viewInput() string {
	title := "GoSilver  · 发送模式"
	b := titleStyle.Render(title) + "\n\n"
	if m.mode == modeSendInputFile {
		b += labelStyle.Render("请输入要分发的文件路径：") + "\n"
	} else {
		b += labelStyle.Render("文件: ") + m.filePath + "\n"
		b += labelStyle.Render("请输入接收端保存目录（不存在会自动创建，可留空）：") + "\n"
	}
	b += m.input.View() + "\n"
	b += m.errLine()
	b += "\n" + hintStyle.Render("[enter] 确认   [esc] 返回接收模式   [ctrl+c] 退出")
	return b
}

func (m *Model) viewServing() string {
	b := titleStyle.Render("GoSilver  · 发送模式（分发中）") + "\n\n"
	b += labelStyle.Render("文件: ") + m.servingName +
		fmt.Sprintf("  (%.1f MB)\n", float64(m.servingSize)/(1<<20))
	b += labelStyle.Render("监听: ") + fmt.Sprintf(":%d", senderGSPPort) +
		labelStyle.Render("   保存目录: ") + dirOrDefault(m.saveDir) + "\n"
	b += okStyle.Render("正在向局域网广播分发通告…") + "\n\n"

	b += labelStyle.Render("节点列表:") + "\n"
	if len(m.peers) == 0 {
		b += hintStyle.Render("  （暂无接收端，等待节点加入…）") + "\n"
	} else {
		for _, p := range m.peers {
			barW := 20
			filled := int(p.Percentage / 100 * float64(barW))
			bar := ""
			for i := 0; i < barW; i++ {
				if i < filled {
					bar += "█"
				} else {
					bar += "░"
				}
			}
			b += fmt.Sprintf("  %-21s %s %5.1f%%  %d Mbps\n",
				p.Addr, bar, p.Percentage, p.MaxSpeed)
		}
	}

	b += m.errLine()
	b += "\n" + hintStyle.Render("[r] 返回接收模式   [q] 退出")
	return borderStyle.Render(b)
}

func (m *Model) errLine() string {
	if m.err != nil {
		return "\n" + errStyle.Render("⚠ "+m.err.Error()) + "\n"
	}
	return ""
}

func dirOrDefault(d string) string {
	if d == "" {
		return "(接收端默认)"
	}
	return d
}
