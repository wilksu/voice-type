package settingsweb

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"voice-type/internal/config"
	"voice-type/internal/kwin"
	"voice-type/internal/polish"
)

//go:embed index.html
var indexHTML string

var (
	mu        sync.Mutex
	activeURL string
)

type savePayload struct {
	HotkeyKey        string   `json:"hotkey_key"`
	HotkeyMode       string   `json:"hotkey_mode"`
	AsrEngine        string   `json:"asr_engine"`
	AsrPreviewEngine string   `json:"asr_preview_engine"`
	AsrLanguage      string   `json:"asr_language"`
	PasteShortcut    string   `json:"paste_shortcut"`
	RestoreClipboard bool     `json:"restore_clipboard"`
	NotifyMode       string   `json:"notify_mode"`
	HudBottomPercent float64  `json:"hud_bottom_percent"`
	PolishEnabled    bool     `json:"polish_enabled"`
	BaseURL          string   `json:"base_url"`
	APIKey           string   `json:"api_key"`
	Model            string   `json:"model"`
	MinCharsForLLM   int      `json:"min_chars_for_llm"`
	Domains          []string `json:"domains"`
	Glossary         string   `json:"glossary"`
	Replacements     string   `json:"replacements"`
	SystemPrompt     string   `json:"system_prompt"`
	LogLevel         string   `json:"log_level"`
	LogTranscript    bool     `json:"log_transcript"`
}

func Open(cfgPath string, onReload func()) {
	mu.Lock()
	if activeURL != "" {
		url := activeURL
		mu.Unlock()
		_ = exec.Command("xdg-open", url).Start()
		return
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		mu.Unlock()
		log.Printf("Failed to start settings web server: %v", err)
		return
	}
	activeURL = "http://" + ln.Addr().String()
	url := activeURL
	mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		promptJSON, _ := json.Marshal(polish.DefaultSystemPrompt)
		page := strings.Replace(indexHTML, "__DEFAULT_PROMPT__", string(promptJSON), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		cfg, _ := config.Load(cfgPath)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			config.AppConfig
			Engines []config.EngineStatus `json:"engines"`
		}{
			AppConfig: cfg,
			Engines:   config.EngineStatuses(),
		})
	})

	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		base := strings.TrimRight(strings.TrimSpace(reqBody.BaseURL), "/")
		if base == "" {
			base = "http://127.0.0.1:1234/v1"
		}
		req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
		if err != nil {
			writeJSONErr(w, 400, err.Error())
			return
		}
		if reqBody.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+reqBody.APIKey)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			writeJSONErr(w, 502, err.Error())
			return
		}
		defer resp.Body.Close()

		var parsed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			writeJSONErr(w, 502, err.Error())
			return
		}
		var models []string
		for _, item := range parsed.Data {
			if item.ID != "" && !strings.Contains(strings.ToLower(item.ID), "embed") {
				models = append(models, item.ID)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	})

	mux.HandleFunc("/api/save", func(w http.ResponseWriter, r *http.Request) {
		var p savePayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeJSONErr(w, 400, err.Error())
			return
		}
		cfg, _ := config.Load(cfgPath)
		if p.HotkeyKey != "" {
			cfg.Hotkey.Key = p.HotkeyKey
		}
		if p.HotkeyMode != "" {
			cfg.Hotkey.Mode = p.HotkeyMode
		}
		if p.AsrEngine != "" && p.AsrEngine != cfg.Asr.Engine {
			cfg.Asr.Engine = p.AsrEngine
			cfg.Asr.ModelDir = config.DefaultModelDirForEngine(p.AsrEngine)
		}
		if p.AsrPreviewEngine != "" {
			if p.AsrPreviewEngine == "off" {
				cfg.Asr.PreviewEngine = "off"
				cfg.Asr.PreviewModelDir = ""
			} else if p.AsrPreviewEngine != cfg.Asr.PreviewEngine || cfg.Asr.PreviewModelDir == "" {
				cfg.Asr.PreviewEngine = p.AsrPreviewEngine
				cfg.Asr.PreviewModelDir = config.DefaultModelDirForEngine(p.AsrPreviewEngine)
			}
		}
		fallback := config.NormalizeAvailableEngines(&cfg.Asr)
		if p.AsrLanguage != "" {
			cfg.Asr.Language = p.AsrLanguage
		}
		if p.PasteShortcut != "" {
			cfg.Injector.PasteShortcut = p.PasteShortcut
		}
		cfg.Injector.RestoreClipboard = p.RestoreClipboard
		cfg.Notify.Enabled = p.NotifyMode != "off"
		if p.NotifyMode != "" {
			cfg.Notify.Mode = p.NotifyMode
		}
		if p.HudBottomPercent >= 5 && p.HudBottomPercent <= 35 {
			cfg.Notify.HudBottomPercent = p.HudBottomPercent
		}
		cfg.Polish.Enabled = p.PolishEnabled
		if p.BaseURL != "" {
			cfg.Polish.BaseURL = p.BaseURL
		}
		cfg.Polish.APIKey = p.APIKey
		cfg.Polish.Model = p.Model
		if p.MinCharsForLLM > 0 {
			cfg.Polish.MinCharsForLLM = p.MinCharsForLLM
		}
		cfg.Polish.Domains = p.Domains
		cfg.Polish.Glossary = strings.TrimSpace(p.Glossary)
		cfg.Polish.Replacements = strings.TrimSpace(p.Replacements)
		if p.SystemPrompt != "" && p.SystemPrompt != polish.DefaultSystemPrompt {
			cfg.Polish.SystemPrompt = p.SystemPrompt
		} else {
			cfg.Polish.SystemPrompt = ""
		}
		if p.LogLevel != "" {
			cfg.Logging.Level = p.LogLevel
		}
		cfg.Logging.LogTranscript = p.LogTranscript

		target := config.DefaultConfigPath()
		if cfgPath != "" {
			target = config.ExpandHome(cfgPath)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			writeJSONErr(w, 500, err.Error())
			return
		}
		data, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(target, append(data, '\n'), 0o644); err != nil {
			writeJSONErr(w, 500, err.Error())
			return
		}

		_, _ = kwin.InstallRule(cfg.Notify.HudBottomPercent)
		if onReload != nil {
			go onReload()
		}

		msg := "已保存并立即生效"
		if fallback {
			msg = "已保存（缺失模型已自动切换为当前可用模型）"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
	})

	go func() {
		srv := &http.Server{Handler: mux}
		_ = srv.Serve(ln)
	}()

	log.Printf("Settings UI available at %s", url)
	_ = exec.Command("xdg-open", url).Start()
}

func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprint(msg)})
}
