package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"voice-type/internal/app"
	"voice-type/internal/config"
	"voice-type/internal/hotkey"
	"voice-type/internal/injector"
	"voice-type/internal/modeldl"
	"voice-type/internal/polish"
	"voice-type/internal/service"
)

func main() {
	log.SetFlags(log.Ltime)

	cfgPath := flag.String("config", config.DefaultConfigPath(), "Path to config JSON")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "run":
		os.Exit(cmdRun(*cfgPath))
	case "settings":
		app.OpenSettingsWindow(*cfgPath)
		select {}
	case "doctor":
		os.Exit(cmdDoctor(*cfgPath))
	case "init-config":
		os.Exit(cmdInitConfig(*cfgPath, len(args) > 1 && args[1] == "--force"))
	case "download-model":
		os.Exit(cmdDownloadModel(args[1:]))
	case "service":
		os.Exit(cmdService(args[1:]))
	case "test-polish":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: voice-type test-polish <text>")
			os.Exit(1)
		}
		os.Exit(cmdTestPolish(*cfgPath, strings.Join(args[1:], " ")))
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: voice-type [-config PATH] <command>")
	fmt.Println("Commands:")
	fmt.Println("  run              Start voice-type daemon + tray + HUD")
	fmt.Println("  settings         Open graphical settings window")
	fmt.Println("  doctor           Check dependencies, devices, models, and LLM")
	fmt.Println("  init-config      Write default config JSON")
	fmt.Println("  download-model   Download SenseVoice-Small INT8 model (ModelScope)")
	fmt.Println("  service <action> Manage systemd user service (install|uninstall|start|stop|restart|status|logs)")
	fmt.Println("  test-polish      Test Doubao-style LLM polishing")
}

func cmdRun(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("Load config failed: %v", err)
	}
	application := app.New(cfgPath, cfg)

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			if sig == syscall.SIGHUP {
				application.Reload()
				continue
			}
			application.Stop()
			go func() {
				time.Sleep(2 * time.Second)
				os.Exit(0)
			}()
			return
		}
	}()

	if err := application.Run(); err != nil {
		log.Printf("Error: %v", err)
		return 1
	}
	return 0
}

func cmdDownloadModel(args []string) int {
	fs := flag.NewFlagSet("download-model", flag.ExitOnError)
	source := fs.String("source", "modelscope", "modelscope or hf-mirror")
	force := fs.Bool("force", false, "Re-download even if present")
	_ = fs.Parse(args)

	if err := modeldl.DownloadSenseVoice("", *source, *force); err != nil {
		log.Printf("Download failed: %v", err)
		return 1
	}
	return 0
}

func cmdService(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: voice-type service <install|uninstall|start|stop|restart|status|logs>")
		return 1
	}
	var err error
	switch args[0] {
	case "install":
		err = service.Install()
	case "uninstall":
		err = service.Uninstall()
	default:
		err = service.Control(args[0])
	}
	if err != nil {
		log.Printf("Service command failed: %v", err)
		return 1
	}
	return 0
}

func cmdDoctor(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("Config error: %v", err)
		return 1
	}
	ok := true
	fmt.Println("=== voice-type (Go) environment check ===")
	for _, tool := range []string{"pw-record", "wl-copy", "wl-paste", "ydotool", "ydotoold", "notify-send"} {
		if p, err := exec.LookPath(tool); err == nil {
			fmt.Printf("  tool %-12s: OK (%s)\n", tool, p)
		} else {
			fmt.Printf("  tool %-12s: MISSING\n", tool)
			if tool == "pw-record" || tool == "wl-copy" || tool == "ydotool" {
				ok = false
			}
		}
	}

	sock := injector.SocketPath()
	if injector.IsSocketAlive(sock) {
		fmt.Printf("  ydotoold socket  : OK (%s)\n", sock)
	} else {
		fmt.Printf("  ydotoold socket  : NOT RUNNING (will auto-start on run) (%s)\n", sock)
	}

	code, err := hotkey.ResolveKeycode(cfg.Hotkey.Key)
	if err != nil {
		fmt.Printf("  evdev (%s): ERROR (%v)\n", cfg.Hotkey.Key, err)
		ok = false
	} else {
		devs := hotkey.FindKeyboardDevices(code)
		for _, d := range devs {
			_ = d.Close()
		}
		if len(devs) > 0 {
			fmt.Printf("  evdev (%s): OK (%d readable device(s))\n", cfg.Hotkey.Key, len(devs))
		} else {
			fmt.Printf("  evdev (%s): NO READABLE KEYBOARDS\n", cfg.Hotkey.Key)
			ok = false
		}
	}

	modelDir := cfg.Asr.ResolvedModelDir()
	_, tokErr := os.Stat(filepath.Join(modelDir, "tokens.txt"))
	_, int8Err := os.Stat(filepath.Join(modelDir, "model.int8.onnx"))
	_, fp32Err := os.Stat(filepath.Join(modelDir, "model.onnx"))
	if tokErr == nil && (int8Err == nil || fp32Err == nil) {
		fmt.Printf("  SenseVoice model : OK (%s)\n", modelDir)
	} else {
		fmt.Printf("  SenseVoice model : MISSING (%s)\n", modelDir)
		ok = false
	}

	if cfg.Polish.Enabled {
		pol := polish.NewDoubaoPolisher(cfg.Polish)
		if m := pol.ResolveModel(); m != "" {
			fmt.Printf("  LLM polish server: OK (%s, model=%s)\n", cfg.Polish.BaseURL, m)
		} else {
			fmt.Printf("  LLM polish server: NOT REACHABLE (%s)\n", cfg.Polish.BaseURL)
		}
	} else {
		fmt.Println("  LLM polish server: DISABLED")
	}

	if ok {
		return 0
	}
	return 1
}

func cmdInitConfig(cfgPath string, force bool) int {
	p, err := config.SaveDefault(cfgPath, force)
	if err != nil {
		log.Printf("Failed: %v", err)
		return 1
	}
	fmt.Printf("Config written to %s\n", p)
	return 0
}

func cmdTestPolish(cfgPath, text string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("Config error: %v", err)
		return 1
	}
	pol := polish.NewDoubaoPolisher(cfg.Polish)
	fmt.Printf("Raw     : %s\n", text)
	fmt.Printf("Polished: %s\n", pol.Polish(text))
	return 0
}
