package asr

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"voice-type/internal/config"
)

var (
	senseVoiceTagRe = regexp.MustCompile(`<\|[^|]+\|>`)
	multiSpaceRe    = regexp.MustCompile(`[ \t]+`)
)

func CleanSenseVoiceText(text string) string {
	cleaned := senseVoiceTagRe.ReplaceAllString(text, "")
	cleaned = multiSpaceRe.ReplaceAllString(cleaned, " ")
	return strings.TrimSpace(cleaned)
}

// SherpaASR implements Recognizer for sherpa-onnx offline models
// (SenseVoice, FireRedASR, and Paraformer).
type SherpaASR struct {
	cfg        config.AsrConfig
	mu         sync.Mutex
	recognizer *sherpa.OfflineRecognizer
}

func NewSherpaASR(cfg config.AsrConfig) *SherpaASR {
	return &SherpaASR{cfg: cfg}
}

func firstExisting(dir string, names ...string) (string, bool) {
	for _, name := range names {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

func (a *SherpaASR) resolveModelConfig() (sherpa.OfflineRecognizerConfig, error) {
	var recCfg sherpa.OfflineRecognizerConfig
	if err := config.ValidateModelDir(a.cfg.Engine, a.cfg.ModelDir); err != nil {
		return recCfg, err
	}
	dir := a.cfg.ResolvedModelDir()

	recCfg.FeatConfig.SampleRate = 16000
	recCfg.FeatConfig.FeatureDim = 80
	recCfg.ModelConfig.NumThreads = a.cfg.NumThreads
	recCfg.ModelConfig.Provider = "cpu"

	engine := strings.ToLower(strings.TrimSpace(a.cfg.Engine))
	if engine == "" {
		engine = "firered"
	}
	if engine != "qwen3-asr" {
		recCfg.ModelConfig.Tokens = filepath.Join(dir, "tokens.txt")
	}

	switch engine {
	case "firered":
		encNames := []string{"encoder.int8.onnx", "encoder.onnx"}
		decNames := []string{"decoder.int8.onnx", "decoder.onnx"}
		if !a.cfg.UseInt8 {
			encNames = []string{"encoder.onnx", "encoder.int8.onnx"}
			decNames = []string{"decoder.onnx", "decoder.int8.onnx"}
		}
		enc, ok1 := firstExisting(dir, encNames...)
		dec, ok2 := firstExisting(dir, decNames...)
		if !ok1 || !ok2 {
			return recCfg, fmt.Errorf("FireRedASR encoder/decoder ONNX files not found in %s", dir)
		}
		recCfg.ModelConfig.FireRedAsr.Encoder = enc
		recCfg.ModelConfig.FireRedAsr.Decoder = dec

	case "paraformer":
		names := []string{"model.int8.onnx", "model.onnx"}
		if !a.cfg.UseInt8 {
			names = []string{"model.onnx", "model.int8.onnx"}
		}
		modelPath, ok := firstExisting(dir, names...)
		if !ok {
			return recCfg, fmt.Errorf("Paraformer model.int8.onnx/model.onnx not found in %s", dir)
		}
		recCfg.ModelConfig.Paraformer.Model = modelPath

	case "qwen3-asr":
		encNames := []string{"encoder.int8.onnx", "encoder.onnx"}
		decNames := []string{"decoder.int8.onnx", "decoder.onnx"}
		if !a.cfg.UseInt8 {
			encNames = []string{"encoder.onnx", "encoder.int8.onnx"}
			decNames = []string{"decoder.onnx", "decoder.int8.onnx"}
		}
		convPath, ok0 := firstExisting(dir, "conv_frontend.onnx")
		enc, ok1 := firstExisting(dir, encNames...)
		dec, ok2 := firstExisting(dir, decNames...)
		if !ok0 || !ok1 || !ok2 {
			return recCfg, fmt.Errorf("Qwen3-ASR ONNX files not found in %s", dir)
		}
		recCfg.ModelConfig.Qwen3ASR.ConvFrontend = convPath
		recCfg.ModelConfig.Qwen3ASR.Encoder = enc
		recCfg.ModelConfig.Qwen3ASR.Decoder = dec
		recCfg.ModelConfig.Qwen3ASR.Tokenizer = filepath.Join(dir, "tokenizer")
		recCfg.ModelConfig.Qwen3ASR.MaxTotalLen = 512
		recCfg.ModelConfig.Qwen3ASR.MaxNewTokens = 256
		recCfg.ModelConfig.Qwen3ASR.Temperature = 0
		recCfg.ModelConfig.Qwen3ASR.TopP = 0.8
		recCfg.ModelConfig.Qwen3ASR.Seed = 42

	default: // "sensevoice"
		names := []string{"model.int8.onnx", "model.onnx"}
		if !a.cfg.UseInt8 {
			names = []string{"model.onnx", "model.int8.onnx"}
		}
		modelPath, ok := firstExisting(dir, names...)
		if !ok {
			return recCfg, fmt.Errorf("SenseVoice model.int8.onnx/model.onnx not found in %s", dir)
		}
		recCfg.ModelConfig.SenseVoice.Model = modelPath
		recCfg.ModelConfig.SenseVoice.Language = a.cfg.Language
		if a.cfg.UseITN {
			recCfg.ModelConfig.SenseVoice.UseInverseTextNormalization = 1
		}
	}
	return recCfg, nil
}

func (a *SherpaASR) Load() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recognizer != nil {
		return nil
	}

	recCfg, err := a.resolveModelConfig()
	if err != nil {
		return err
	}

	rec := sherpa.NewOfflineRecognizer(&recCfg)
	if rec == nil {
		return fmt.Errorf("failed to create sherpa-onnx OfflineRecognizer (%s)", a.cfg.Engine)
	}
	a.recognizer = rec
	return nil
}

func (a *SherpaASR) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(a.recognizer)
		a.recognizer = nil
	}
}

func (a *SherpaASR) Transcribe(samples []float32, sampleRate int) (string, error) {
	if len(samples) == 0 {
		return "", nil
	}
	if err := a.Load(); err != nil {
		return "", err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recognizer == nil {
		return "", fmt.Errorf("recognizer is closed")
	}

	// Pad 250ms silence before and after so short utterances don't clip edge phonemes
	pad := sampleRate / 4
	padded := make([]float32, len(samples)+pad*2)
	copy(padded[pad:], samples)

	stream := sherpa.NewOfflineStream(a.recognizer)
	defer sherpa.DeleteOfflineStream(stream)
	stream.AcceptWaveform(sampleRate, padded)
	a.recognizer.Decode(stream)
	res := stream.GetResult()
	return CleanSenseVoiceText(res.Text), nil
}
