package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DefaultConfigRel          = ".config/voice-type/config.json"
	DefaultFireRedModelRel    = ".local/share/voice-type/models/sherpa-onnx-fire-red-asr-large-zh_en-2025-02-16"
	DefaultSenseVoiceModelRel = ".local/share/voice-type/models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17"
	DefaultParaformerModelRel = ".local/share/voice-type/models/sherpa-onnx-paraformer-zh-2024-03-09"
	DefaultQwen3ASRModelRel   = ".local/share/voice-type/models/sherpa-onnx-qwen3-asr-0.6B-int8"
	DefaultWhisperModelRel    = ".local/share/voice-type/models/whisper"
	DefaultModelRel           = DefaultFireRedModelRel
)

type HotkeyConfig struct {
	Key            string  `json:"key"`
	Mode           string  `json:"mode"`
	MinDurationSec float64 `json:"min_duration_sec"`
}

type AsrConfig struct {
	Engine          string `json:"engine"` // "qwen3-asr" | "sensevoice" | "whisper" | "firered" | "paraformer"
	ModelDir        string `json:"model_dir"`
	PreviewEngine   string `json:"preview_engine,omitempty"` // "off" | "sensevoice" | "qwen3-asr" | "paraformer"
	PreviewModelDir string `json:"preview_model_dir,omitempty"`
	NumThreads      int    `json:"num_threads"`
	Language        string `json:"language"`
	UseITN          bool   `json:"use_itn"`
	UseInt8         bool   `json:"use_int8"`
}

func (a AsrConfig) PreviewConfig() AsrConfig {
	p := a
	p.Engine = a.PreviewEngine
	if a.PreviewModelDir != "" {
		p.ModelDir = a.PreviewModelDir
	} else {
		p.ModelDir = DefaultModelDirForEngine(a.PreviewEngine)
	}
	return p
}

func DefaultModelDirForEngine(engine string) string {
	return "~/" + LookupEngine(engine).DefaultDirRel
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
	Replacements   string   `json:"replacements,omitempty"`
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

func defaultThreads() int {
	n := runtime.NumCPU() / 2
	if n < 4 {
		return 4
	}
	if n > 8 {
		return 8
	}
	return n
}

func DefaultConfig() AppConfig {
	return AppConfig{
		Hotkey: HotkeyConfig{
			Key:            "KEY_RIGHTALT",
			Mode:           "push_to_talk",
			MinDurationSec: 0.3,
		},
		Asr: AsrConfig{
			Engine:     "firered",
			ModelDir:   "~/" + DefaultFireRedModelRel,
			NumThreads: defaultThreads(),
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
			Replacements:   "够语言=Go语言, 购语言=Go语言, 过语言=Go语言, 构编程语言=Go语言, 技术战=技术栈",
		},
		Injector: InjectorConfig{
			PasteShortcut:    "ctrl+v",
			PasteDelayMs:     60,
			RestoreClipboard: true,
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
			NormalizeAvailableEngines(&cfg.Asr)
			return cfg, nil
		}
		return cfg, err
	}

	// Clear Engine before unmarshaling so legacy configs without "engine"
	// can infer the matching engine from their existing model_dir.
	cfg.Asr.Engine = ""
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Asr.Engine == "" {
		cfg.Asr.Engine = InferEngineFromModelDir(cfg.Asr.ModelDir)
	}
	if cfg.Asr.ModelDir == "" {
		cfg.Asr.ModelDir = DefaultModelDirForEngine(cfg.Asr.Engine)
	}
	if cfg.Asr.NumThreads <= 4 && defaultThreads() > 4 {
		cfg.Asr.NumThreads = defaultThreads()
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
	NormalizeAvailableEngines(&cfg.Asr)
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
