package injector

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"voice-type/internal/config"
)

const (
	keyLeftCtrl         = 29
	keyLeftShift        = 42
	keyV                = 47
	maxClipboardBackupB = 4 * 1024 * 1024 // 4MB cap to stay lightweight
)

type WaylandTextInjector struct {
	cfg         config.InjectorConfig
	mu          sync.Mutex
	ydotooldCmd *exec.Cmd
	pasteSeq    atomic.Int64
}

func NewWaylandTextInjector(cfg config.InjectorConfig) *WaylandTextInjector {
	return &WaylandTextInjector{cfg: cfg}
}

func (w *WaylandTextInjector) UpdateConfig(cfg config.InjectorConfig) {
	w.mu.Lock()
	w.cfg = cfg
	w.mu.Unlock()
}

func SocketPath() string {
	if s := os.Getenv("YDOTOOL_SOCKET"); s != "" {
		return s
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return filepath.Join(runtimeDir, ".ydotool_socket")
}

func IsSocketAlive(sock string) bool {
	if _, err := os.Stat(sock); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unixgram", sock, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (w *WaylandTextInjector) CheckDependencies() []string {
	var missing []string
	for _, tool := range []string{"wl-copy", "ydotool"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	return missing
}

func (w *WaylandTextInjector) EnsureYdotoold() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	sock := SocketPath()
	if IsSocketAlive(sock) {
		return nil
	}
	if _, err := os.Stat(sock); err == nil {
		log.Printf("Removing stale ydotoold socket: %s", sock)
		_ = os.Remove(sock)
	}
	if _, err := exec.LookPath("ydotoold"); err != nil {
		return fmt.Errorf("ydotoold not found and socket does not exist")
	}

	log.Printf("Starting ydotoold (%s)...", sock)
	cmd := exec.Command("ydotoold")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ydotoold: %w", err)
	}
	w.ydotooldCmd = cmd

	for i := 0; i < 25; i++ {
		if IsSocketAlive(sock) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for ydotoold socket at %s (%s)", sock, strings.TrimSpace(stderr.String()))
}

func (w *WaylandTextInjector) Cleanup() {
	w.mu.Lock()
	cmd := w.ydotooldCmd
	w.ydotooldCmd = nil
	w.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
}

type clipboardSnapshot struct {
	data     []byte
	mimeType string
}

func pickPrimaryMime(listOutput string) string {
	var firstValid string
	for _, line := range strings.Split(listOutput, "\n") {
		m := strings.TrimSpace(line)
		if m == "" || m == "TARGETS" || m == "TIMESTAMP" || m == "MULTIPLE" || m == "SAVE_TARGETS" {
			continue
		}
		if m == "text/plain;charset=utf-8" || m == "UTF8_STRING" || m == "text/plain" {
			return m
		}
		if firstValid == "" {
			firstValid = m
		}
	}
	return firstValid
}

func (w *WaylandTextInjector) readClipboard() (*clipboardSnapshot, bool) {
	if _, err := exec.LookPath("wl-paste"); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	typesOut, err := exec.CommandContext(ctx, "wl-paste", "--list-types").Output()
	if err != nil {
		return nil, false
	}
	mime := pickPrimaryMime(string(typesOut))
	if mime == "" {
		return nil, false
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 1000*time.Millisecond)
	defer cancel2()
	out, err := exec.CommandContext(ctx2, "wl-paste", "--no-newline", "--type", mime).Output()
	if err != nil || len(out) > maxClipboardBackupB {
		return nil, false
	}
	return &clipboardSnapshot{data: out, mimeType: mime}, true
}

func (w *WaylandTextInjector) writeClipboard(data []byte, mimeType string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var args []string
	if mimeType != "" {
		args = []string{"--type", mimeType}
	}
	cmd := exec.CommandContext(ctx, "wl-copy", args...)
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

func (w *WaylandTextInjector) clearClipboard() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "wl-copy", "--clear").Run()
}

func (w *WaylandTextInjector) triggerPaste() error {
	if !IsSocketAlive(SocketPath()) {
		if err := w.EnsureYdotoold(); err != nil {
			return err
		}
	}
	shortcut := strings.ToLower(strings.TrimSpace(w.cfg.PasteShortcut))
	var keys []string
	if shortcut == "ctrl+shift+v" {
		keys = []string{
			fmt.Sprintf("%d:1", keyLeftCtrl),
			fmt.Sprintf("%d:1", keyLeftShift),
			fmt.Sprintf("%d:1", keyV),
			fmt.Sprintf("%d:0", keyV),
			fmt.Sprintf("%d:0", keyLeftShift),
			fmt.Sprintf("%d:0", keyLeftCtrl),
		}
	} else {
		keys = []string{
			fmt.Sprintf("%d:1", keyLeftCtrl),
			fmt.Sprintf("%d:1", keyV),
			fmt.Sprintf("%d:0", keyV),
			fmt.Sprintf("%d:0", keyLeftCtrl),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := append([]string{"key"}, keys...)
	out, err := exec.CommandContext(ctx, "ydotool", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ydotool failed (%v): %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (w *WaylandTextInjector) Inject(text string) error {
	if text == "" {
		return nil
	}

	w.mu.Lock()
	cfg := w.cfg
	w.mu.Unlock()

	seq := w.pasteSeq.Add(1)

	var snap *clipboardSnapshot
	var hadPrev bool
	if cfg.RestoreClipboard {
		snap, hadPrev = w.readClipboard()
	}

	if err := w.writeClipboard([]byte(text), ""); err != nil {
		return fmt.Errorf("wl-copy failed: %w", err)
	}
	if cfg.PasteDelayMs > 0 {
		time.Sleep(time.Duration(cfg.PasteDelayMs) * time.Millisecond)
	}

	if err := w.triggerPaste(); err != nil {
		return err
	}

	if cfg.RestoreClipboard {
		delay := cfg.RestoreDelayMs
		if delay < 150 {
			delay = 150
		}
		go func(mySeq int64, prev *clipboardSnapshot, existed bool, wait time.Duration) {
			time.Sleep(wait)
			if w.pasteSeq.Load() != mySeq {
				return
			}
			if existed && prev != nil {
				_ = w.writeClipboard(prev.data, prev.mimeType)
			} else {
				w.clearClipboard()
			}
		}(seq, snap, hadPrev, time.Duration(delay)*time.Millisecond)
	}
	return nil
}
