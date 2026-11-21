package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultConfigRel = ".config/voice-type/config.json"
	DefaultModelRel  = ".local/share/voice-type/models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17"
)

type HotkeyConfig struct {
	Key            string  `json:"key"`
	Mode           string  `json:"mode"`
	MinDurationSec float64 `json:"min_duration_sec"`
}

type AsrConfig struct {
	ModelDir   string `json:"model_dir"`
	NumThreads int    `json:"num_threads"`
	Language   string `json:"language"`
	UseITN     bool   `json:"use_itn"`
	UseInt8    bool   `json:"use_int8"`
}

func (a AsrConfig) ResolvedModelDir() string {
	return ExpandHome(a.ModelDir)
}

type PolishConfig struct {
	Enabled        bool     `json:"enabled"`
	BaseURL        string   `json:"base_url"`
	APIKey         string   `json:"api_key"`
	Model          string   `json:"model"`
	TimeoutSec     float64  `json:"timeout_sec"`
	MinCharsForLLM int      `json:"min_chars_for_llm"`
	Temperature    float64  `json:"temperature"`
	Domains        []string `json:"domains,omitempty"`
	Glossary       string   `json:"glossary,omitempty"`
	SystemPrompt   string   `json:"system_prompt,omitempty"`
}

type InjectorConfig struct {
	PasteShortcut    string `json:"paste_shortcut"`
	PasteDelayMs     int    `json:"paste_delay_ms"`
	RestoreClipboard bool   `json:"restore_clipboard"`
	RestoreDelayMs   int    `json:"restore_delay_ms"`
}

type NotifyConfig struct {
	Enabled          bool    `json:"enabled"`
	Mode             string  `json:"mode"` // "hud" | "osd" | "sound" | "errors_only" | "notify" | "off"
	HudBottomPercent float64 `json:"hud_bottom_percent,omitempty"`
}

type LoggingConfig struct {
	Level         string `json:"level"`          // "error" | "warn" | "info" | "debug"
	LogTranscript bool   `json:"log_transcript"` // default false to avoid leaking sensitive spoken text
}

type AppConfig struct {
	Hotkey   HotkeyConfig   `json:"hotkey"`
	Asr      AsrConfig      `json:"asr"`
	Polish   PolishConfig   `json:"polish"`
	Injector InjectorConfig `json:"injector"`
	Notify   NotifyConfig   `json:"notify"`
	Logging  LoggingConfig  `json:"logging"`
}

func DefaultConfig() AppConfig {
	return AppConfig{
		Hotkey: HotkeyConfig{
			Key:            "KEY_RIGHTALT",
			Mode:           "push_to_talk",
			MinDurationSec: 0.3,
		},
		Asr: AsrConfig{
			ModelDir:   "~/" + DefaultModelRel,
			NumThreads: 4,
			Language:   "auto",
			UseITN:     true,
			UseInt8:    true,
		},
		Polish: PolishConfig{
			Enabled:        true,
			BaseURL:        "http://127.0.0.1:1234/v1",
			APIKey:         "lm-studio",
			Model:          "",
			TimeoutSec:     6.0,
			MinCharsForLLM: 10,
			Temperature:    0.1,
			Domains:        []string{"it", "office"},
		},
		Injector: InjectorConfig{
			PasteShortcut:    "ctrl+v",
			PasteDelayMs:     60,
			RestoreClipboard: false,
			RestoreDelayMs:   350,
		},
		Notify: NotifyConfig{
			Enabled:          true,
			Mode:             "hud",
			HudBottomPercent: 12.0,
		},
		Logging: LoggingConfig{
			Level:         "info",
			LogTranscript: false,
		},
	}
}

func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, DefaultConfigRel)
}

func ExpandHome(p string) string {
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func Load(path string) (AppConfig, error) {
	cfg := DefaultConfig()
	target := DefaultConfigPath()
	if path != "" {
		target = ExpandHome(path)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Notify.Mode == "" {
		cfg.Notify.Mode = "hud"
	}
	if cfg.Notify.HudBottomPercent <= 0 {
		cfg.Notify.HudBottomPercent = 12.0
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	return cfg, nil
}

func SaveDefault(path string, overwrite bool) (string, error) {
	target := DefaultConfigPath()
	if path != "" {
		target = ExpandHome(path)
	}
	if !overwrite {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(DefaultConfig(), "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return "", err
	}
	return target, nil
}
