package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"voice-type/internal/kwin"
)

const ServiceName = "voice-type.service"

func UnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", ServiceName)
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}

	// Ensure KWin HUD rule is installed too (pure Go)
	_, _ = kwin.InstallRule(12.0)

	unit := fmt.Sprintf(`[Unit]
Description=voice-type local bilingual voice input daemon
After=graphical-session.target pipewire.service

[Service]
Type=simple
ExecStart=%s run
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=3

[Install]
WantedBy=graphical-session.target
`, exe)

	unitPath := UnitPath()
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", unitPath)

	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return runCmd("systemctl", "--user", "enable", "--now", ServiceName)
}

func Uninstall() error {
	_ = runCmd("systemctl", "--user", "disable", "--now", ServiceName)
	unitPath := UnitPath()
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = runCmd("systemctl", "--user", "daemon-reload")
	fmt.Printf("Removed %s\n", unitPath)
	return nil
}

func Control(action string) error {
	switch action {
	case "start", "stop", "restart", "status":
		return runCmd("systemctl", "--user", action, ServiceName)
	case "logs":
		return runCmd("journalctl", "--user", "-u", ServiceName, "-f", "--no-pager")
	default:
		return fmt.Errorf("unknown service action: %s", action)
	}
}
