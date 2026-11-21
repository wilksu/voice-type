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

type SenseVoiceASR struct {
	cfg        config.AsrConfig
	mu         sync.Mutex
	recognizer *sherpa.OfflineRecognizer
}

func NewSenseVoiceASR(cfg config.AsrConfig) *SenseVoiceASR {
	return &SenseVoiceASR{cfg: cfg}
}

func (a *SenseVoiceASR) resolveModelFiles() (string, string, error) {
	dir := a.cfg.ResolvedModelDir()
	if _, err := os.Stat(dir); err != nil {
		return "", "", fmt.Errorf("SenseVoice model dir not found: %s", dir)
	}
	tokens := filepath.Join(dir, "tokens.txt")
	if _, err := os.Stat(tokens); err != nil {
		return "", "", fmt.Errorf("tokens.txt not found in %s", dir)
	}

	candidates := []string{"model.int8.onnx", "model.onnx"}
	if !a.cfg.UseInt8 {
		candidates = []string{"model.onnx", "model.int8.onnx"}
	}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, tokens, nil
		}
	}
	return "", "", fmt.Errorf("no model.int8.onnx or model.onnx found in %s", dir)
}

func (a *SenseVoiceASR) Load() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recognizer != nil {
		return nil
	}

	modelPath, tokensPath, err := a.resolveModelFiles()
	if err != nil {
		return err
	}

	recCfg := sherpa.OfflineRecognizerConfig{}
	recCfg.FeatConfig.SampleRate = 16000
	recCfg.FeatConfig.FeatureDim = 80
	recCfg.ModelConfig.SenseVoice.Model = modelPath
	recCfg.ModelConfig.SenseVoice.Language = a.cfg.Language
	if a.cfg.UseITN {
		recCfg.ModelConfig.SenseVoice.UseInverseTextNormalization = 1
	}
	recCfg.ModelConfig.Tokens = tokensPath
	recCfg.ModelConfig.NumThreads = a.cfg.NumThreads
	recCfg.ModelConfig.Provider = "cpu"

	rec := sherpa.NewOfflineRecognizer(&recCfg)
	if rec == nil {
		return fmt.Errorf("failed to create sherpa-onnx OfflineRecognizer from %s", modelPath)
	}
	a.recognizer = rec
	return nil
}

func (a *SenseVoiceASR) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(a.recognizer)
		a.recognizer = nil
	}
}

func (a *SenseVoiceASR) Transcribe(samples []float32, sampleRate int) (string, error) {
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

	stream := sherpa.NewOfflineStream(a.recognizer)
	defer sherpa.DeleteOfflineStream(stream)
	stream.AcceptWaveform(sampleRate, samples)
	a.recognizer.Decode(stream)
	res := stream.GetResult()
	return CleanSenseVoiceText(res.Text), nil
}
