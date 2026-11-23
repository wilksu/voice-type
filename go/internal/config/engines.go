package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EngineSpec describes an ASR engine's metadata, default model layout, and runtime capabilities.
type EngineSpec struct {
	ID                  string
	DisplayName         string
	DefaultDirRel       string
	RequiredFileGroups  [][]string // Each group is a list of acceptable filenames (e.g. int8 or fp32)
	SupportsLivePreview bool
}

var engineRegistry = map[string]EngineSpec{
	"firered": {
		ID:            "firered",
		DisplayName:   "FireRedASR-Large (CPU 高精度)",
		DefaultDirRel: DefaultFireRedModelRel,
		RequiredFileGroups: [][]string{
			{"encoder.int8.onnx", "encoder.onnx"},
			{"decoder.int8.onnx", "decoder.onnx"},
			{"tokens.txt"},
		},
		SupportsLivePreview: false,
	},
	"sensevoice": {
		ID:            "sensevoice",
		DisplayName:   "SenseVoice-Small (CPU 极速)",
		DefaultDirRel: DefaultSenseVoiceModelRel,
		RequiredFileGroups: [][]string{
			{"model.int8.onnx", "model.onnx"},
			{"tokens.txt"},
		},
		SupportsLivePreview: true,
	},
	"paraformer": {
		ID:            "paraformer",
		DisplayName:   "Paraformer-zh (达摩院中文双语)",
		DefaultDirRel: DefaultParaformerModelRel,
		RequiredFileGroups: [][]string{
			{"model.int8.onnx", "model.onnx"},
			{"tokens.txt"},
		},
		SupportsLivePreview: true,
	},
	"qwen3-asr": {
		ID:            "qwen3-asr",
		DisplayName:   "Qwen3-ASR-0.6B (通义大模型识别)",
		DefaultDirRel: DefaultQwen3ASRModelRel,
		RequiredFileGroups: [][]string{
			{"conv_frontend.onnx"},
			{"encoder.int8.onnx", "encoder.onnx"},
			{"decoder.int8.onnx", "decoder.onnx"},
			{"tokenizer/vocab.json"},
		},
		SupportsLivePreview: true,
	},
	"whisper": {
		ID:            "whisper",
		DisplayName:   "Whisper large-v3-turbo (RX 9070 GPU)",
		DefaultDirRel: DefaultWhisperModelRel,
		RequiredFileGroups: [][]string{
			{"ggml-large-v3-turbo-q5_0.bin"},
		},
		SupportsLivePreview: false,
	},
}

// LookupEngine returns the EngineSpec for id, defaulting to "firered" if unknown.
func LookupEngine(id string) EngineSpec {
	key := strings.ToLower(strings.TrimSpace(id))
	if spec, ok := engineRegistry[key]; ok {
		return spec
	}
	return engineRegistry["firered"]
}

// InferEngineFromModelDir guesses the engine ID from a legacy model_dir path.
func InferEngineFromModelDir(modelDir string) string {
	lower := strings.ToLower(modelDir)
	switch {
	case strings.Contains(lower, "sense-voice"):
		return "sensevoice"
	case strings.Contains(lower, "paraformer"):
		return "paraformer"
	case strings.Contains(lower, "qwen3"):
		return "qwen3-asr"
	case strings.Contains(lower, "whisper"):
		return "whisper"
	default:
		return "firered"
	}
}

// ValidateModelDir checks that dir exists and contains all required files for engine.
func ValidateModelDir(engine, dir string) error {
	resolved := ExpandHome(dir)
	if _, err := os.Stat(resolved); err != nil {
		return fmt.Errorf("ASR model dir not found: %s (run `voice-type download-model -engine %s`)", resolved, LookupEngine(engine).ID)
	}
	spec := LookupEngine(engine)
	for _, group := range spec.RequiredFileGroups {
		found := false
		for _, name := range group {
			if _, err := os.Stat(filepath.Join(resolved, name)); err == nil {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s model missing required file (%s) in %s", spec.ID, strings.Join(group, " or "), resolved)
		}
	}
	return nil
}

var (
	enginePriority        = []string{"qwen3-asr", "sensevoice", "whisper", "firered", "paraformer"}
	previewEnginePriority = []string{"sensevoice", "qwen3-asr", "paraformer"}
)

type EngineStatus struct {
	ID                  string `json:"id"`
	DisplayName         string `json:"display_name"`
	Installed           bool   `json:"installed"`
	SupportsLivePreview bool   `json:"supports_live_preview"`
}

// EngineStatuses returns all registered engines in priority order with disk availability.
func EngineStatuses() []EngineStatus {
	out := make([]EngineStatus, 0, len(enginePriority))
	for _, id := range enginePriority {
		spec := LookupEngine(id)
		installed := ValidateModelDir(id, DefaultModelDirForEngine(id)) == nil
		out = append(out, EngineStatus{
			ID:                  spec.ID,
			DisplayName:         spec.DisplayName,
			Installed:           installed,
			SupportsLivePreview: spec.SupportsLivePreview,
		})
	}
	return out
}

func firstInstalledEngine(candidates []string) (EngineSpec, bool) {
	for _, id := range candidates {
		if ValidateModelDir(id, DefaultModelDirForEngine(id)) == nil {
			return LookupEngine(id), true
		}
	}
	return EngineSpec{}, false
}

// NormalizeAvailableEngines ensures cfg.Engine and cfg.PreviewEngine point to
// installed models on disk, automatically falling back to the first available one.
func NormalizeAvailableEngines(cfg *AsrConfig) bool {
	changed := false
	if ValidateModelDir(cfg.Engine, cfg.ModelDir) != nil {
		if spec, ok := firstInstalledEngine(enginePriority); ok {
			cfg.Engine = spec.ID
			cfg.ModelDir = "~/" + spec.DefaultDirRel
			changed = true
		}
	}

	preview := strings.ToLower(strings.TrimSpace(cfg.PreviewEngine))
	if preview == "off" {
		cfg.PreviewEngine = "off"
		cfg.PreviewModelDir = ""
		return changed
	}

	previewDir := cfg.PreviewModelDir
	if previewDir == "" && preview != "" {
		previewDir = DefaultModelDirForEngine(preview)
	}
	previewValid := preview != "" &&
		LookupEngine(preview).SupportsLivePreview &&
		ValidateModelDir(preview, previewDir) == nil

	if !previewValid {
		if spec, ok := firstInstalledEngine(previewEnginePriority); ok {
			cfg.PreviewEngine = spec.ID
			cfg.PreviewModelDir = "~/" + spec.DefaultDirRel
		} else {
			cfg.PreviewEngine = "off"
			cfg.PreviewModelDir = ""
		}
		changed = true
	} else {
		cfg.PreviewEngine = preview
		if cfg.PreviewModelDir == "" {
			cfg.PreviewModelDir = DefaultModelDirForEngine(preview)
		}
	}
	return changed
}
