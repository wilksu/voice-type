package notify

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/godbus/dbus/v5"

	"voice-type/internal/assets"
	"voice-type/internal/config"
)

var startSoundCandidates = []string{
	"/usr/share/sounds/ocean/stereo/audio-volume-change.oga",
	"/usr/share/sounds/freedesktop/stereo/audio-volume-change.oga",
	"/usr/share/sounds/ocean/stereo/button-pressed.oga",
}

type hudClient struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func findHudScript() (string, string) {
	py, err := exec.LookPath("python3")
	if err != nil {
		return "", ""
	}
	check := exec.Command(py, "-c", "import gi; gi.require_version('Gtk','4.0'); from gi.repository import Gtk")
	if err := check.Run(); err != nil {
		log.Printf("Python GTK4 not available (%v); falling back to sound-only feedback", err)
		return "", ""
	}
	if script, err := assets.ScriptPath("hud.py"); err == nil {
		return py, script
	}
	return "", ""
}

func newHudClient() *hudClient {
	py, script := findHudScript()
	if py == "" || script == "" {
		log.Printf("GTK4 HUD script not found; falling back to quiet mode")
		return nil
	}
	cmd := exec.Command(py, script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("Failed to open HUD stdin: %v", err)
		return nil
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start GTK4 HUD: %v", err)
		return nil
	}
	return &hudClient{cmd: cmd, stdin: stdin}
}

func (h *hudClient) send(payload map[string]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stdin == nil {
		return
	}
	b, _ := json.Marshal(payload)
	if _, err := h.stdin.Write(append(b, '\n')); err != nil {
		// HUD subprocess died; clean up and attempt one restart
		_ = h.stdin.Close()
		h.stdin = nil
		if h.cmd != nil {
			_ = h.cmd.Wait()
			h.cmd = nil
		}
		if restarted := newHudClient(); restarted != nil {
			h.cmd = restarted.cmd
			h.stdin = restarted.stdin
			_, _ = h.stdin.Write(append(b, '\n'))
		}
	}
}

func (h *hudClient) show(state, text string) {
	h.send(map[string]string{"action": "show", "state": state, "text": text})
}

func (h *hudClient) hide() {
	h.send(map[string]string{"action": "hide"})
}

func (h *hudClient) close() {
	if h == nil {
		return
	}
	h.send(map[string]string{"action": "quit"})
	h.mu.Lock()
	if h.stdin != nil {
		_ = h.stdin.Close()
		h.stdin = nil
	}
	cmd := h.cmd
	h.cmd = nil
	h.mu.Unlock()
	if cmd != nil {
		_ = cmd.Wait()
	}
}

type StatusNotifier struct {
	enabled    bool
	mode       string
	hasNotify  bool
	player     string
	startSound string
	hud        *hudClient
	dbusConn   *dbus.Conn
}

func NewStatusNotifier(cfg config.NotifyConfig) *StatusNotifier {
	mode := "off"
	if cfg.Enabled {
		mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
		if mode == "" {
			mode = "hud"
		}
	}

	_, err := exec.LookPath("notify-send")
	hasNotify := err == nil

	player := ""
	if p, err := exec.LookPath("paplay"); err == nil {
		player = p
	} else if p, err := exec.LookPath("pw-play"); err == nil {
		player = p
	}

	soundPath := ""
	for _, p := range startSoundCandidates {
		if _, err := os.Stat(p); err == nil {
			soundPath = p
			break
		}
	}

	var hud *hudClient
	if mode == "hud" {
		hud = newHudClient()
	}

	var conn *dbus.Conn
	if mode == "osd" {
		if c, err := dbus.SessionBus(); err == nil {
			conn = c
		}
	}

	return &StatusNotifier{
		enabled:    cfg.Enabled,
		mode:       mode,
		hasNotify:  hasNotify,
		player:     player,
		startSound: soundPath,
		hud:        hud,
		dbusConn:   conn,
	}
}

func runDetached(name string, args ...string) {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}

func (n *StatusNotifier) playSound() {
	if !n.enabled || n.player == "" || n.startSound == "" {
		return
	}
	runDetached(n.player, n.startSound)
}

func (n *StatusNotifier) osdShow(icon, text string) {
	if n.dbusConn == nil {
		return
	}
	obj := n.dbusConn.Object("org.kde.plasmashell", "/org/kde/osdService")
	_ = obj.Call("org.kde.osdService.showText", 0, icon, text).Err
}

func (n *StatusNotifier) osdHide() {
	if n.dbusConn == nil {
		return
	}
	obj := n.dbusConn.Object("org.kde.plasmashell", "/org/kde/osdService")
	_ = obj.Call("org.kde.osdService.hide", 0).Err
}

func (n *StatusNotifier) Show(title, body string, timeoutMs int) {
	if !n.enabled || n.mode == "off" || !n.hasNotify {
		return
	}
	args := []string{
		"--app-name=voice-type",
		"--transient",
		"--expire-time=" + itoa(timeoutMs),
		"--hint=string:x-canonical-private-synchronous:voice-type-status",
		title,
	}
	if body != "" {
		args = append(args, body)
	}
	runDetached("notify-send", args...)
}

func (n *StatusNotifier) OnReady(hotkey string) {
	if n.mode == "notify" {
		n.Show("voice-type 已就绪", "按住 "+hotkey+" 说话", 2500)
	}
}

func (n *StatusNotifier) OnListeningStart() {
	switch n.mode {
	case "hud":
		n.playSound()
		n.hud.show("listening", "正在聆听...")
	case "osd":
		n.playSound()
		n.osdShow("audio-input-microphone", "正在聆听...")
	case "sound":
		n.playSound()
	case "notify":
		n.Show("正在聆听...", "松开按键开始识别与润色", 10000)
	}
}

func (n *StatusNotifier) OnListeningPreview(text string) {
	if text == "" {
		return
	}
	switch n.mode {
	case "hud":
		n.hud.show("listening", text)
	case "osd":
		n.osdShow("audio-input-microphone", text)
	}
}

func (n *StatusNotifier) OnProcessing() {
	switch n.mode {
	case "hud":
		n.hud.show("processing", "识别与润色中...")
	case "osd":
		n.osdShow("document-edit", "识别与润色中...")
	case "notify":
		n.Show("识别与润色中...", "", 5000)
	}
}

func (n *StatusNotifier) OnDone(preview string) {
	switch n.mode {
	case "hud":
		n.hud.hide()
	case "osd":
		n.osdHide()
	case "notify":
		n.Show("已上屏", preview, 2000)
	}
}

func (n *StatusNotifier) OnError(title, body string) {
	n.hud.hide()
	n.osdHide()
	if n.mode != "off" {
		n.Show(title, body, 3000)
	}
}

func (n *StatusNotifier) Close() {
	n.hud.close()
}

func itoa(v int) string {
	if v <= 0 {
		return "2000"
	}
	// simple positive int formatting
	buf := [12]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
