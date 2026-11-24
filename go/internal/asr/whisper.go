package asr

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"voice-type/internal/config"
	"voice-type/internal/logger"
)

const (
	whisperContainerName = "voice-type-whisper"
	whisperImage         = "ghcr.io/ggml-org/whisper.cpp:main-vulkan"
	whisperModelFile     = "ggml-large-v3-turbo-q5_0.bin"
	whisperDefaultURL    = "http://127.0.0.1:8178"
)

// Recognizer is the common interface implemented by local sherpa-onnx and whisper-server backends.
type Recognizer interface {
	Load() error
	Transcribe(samples []float32, sampleRate int) (string, error)
	Close()
}

func NewRecognizer(cfg config.AsrConfig, glossary string) Recognizer {
	if strings.EqualFold(strings.TrimSpace(cfg.Engine), "whisper") {
		return NewWhisperASR(cfg, glossary)
	}
	return NewSherpaASR(cfg)
}

type WhisperASR struct {
	cfg         config.AsrConfig
	glossary    string
	serverURL   string
	mu          sync.Mutex
	startedByUs bool
	client      *http.Client
}

func NewWhisperASR(cfg config.AsrConfig, glossary string) *WhisperASR {
	return &WhisperASR{
		cfg:       cfg,
		glossary:  strings.TrimSpace(glossary),
		serverURL: whisperDefaultURL,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (w *WhisperASR) isAlive() bool {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(w.serverURL + "/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 500
}

func (w *WhisperASR) Load() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.isAlive() {
		return nil
	}

	modelDir := w.cfg.ResolvedModelDir()
	modelPath := filepath.Join(modelDir, whisperModelFile)
	if _, err := os.Stat(modelPath); err != nil {
		return fmt.Errorf("Whisper model not found: %s (run `voice-type download-model -engine whisper`)", modelPath)
	}
	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("podman not found; required to run %s", whisperImage)
	}

	// Clean up any leftover container with the same name
	_ = exec.Command("podman", "rm", "-f", whisperContainerName).Run()

	lang := strings.TrimSpace(w.cfg.Language)
	if lang == "" {
		lang = "zh"
	}

	logger.Infof("Starting whisper-server Vulkan container (%s)...", whisperImage)
	args := []string{
		"run", "-d", "--rm",
		"--name", whisperContainerName,
		"--device", "/dev/dri",
		"--security-opt", "label=disable",
		"--network=host",
		"-v", modelDir + ":/models:ro",
		whisperImage,
		"whisper-server",
		"--host", "127.0.0.1",
		"--port", "8178",
		"-m", "/models/" + whisperModelFile,
		"-l", lang,
		"-sns",
	}
	out, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start whisper-server container (%v): %s", err, strings.TrimSpace(string(out)))
	}
	w.startedByUs = true

	for i := 0; i < 60; i++ {
		if w.isAlive() {
			logger.Infof("whisper-server Vulkan ready at %s", w.serverURL)
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for whisper-server at %s", w.serverURL)
}

func (w *WhisperASR) Close() {
	w.mu.Lock()
	started := w.startedByUs
	w.startedByUs = false
	w.mu.Unlock()

	if started {
		_ = exec.Command("podman", "stop", "-t", "2", whisperContainerName).Run()
	}
}

func encodeWAV(samples []float32, sampleRate int) []byte {
	dataSize := len(samples) * 2
	buf := bytes.NewBuffer(make([]byte, 0, 44+dataSize))
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(buf, binary.LittleEndian, uint16(1)) // mono
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(dataSize))
	for _, s := range samples {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		_ = binary.Write(buf, binary.LittleEndian, int16(s*32767.0))
	}
	return buf.Bytes()
}

func (w *WhisperASR) Transcribe(samples []float32, sampleRate int) (string, error) {
	if len(samples) == 0 {
		return "", nil
	}
	if err := w.Load(); err != nil {
		return "", err
	}

	// Pad 200ms silence on edges
	pad := sampleRate / 5
	padded := make([]float32, len(samples)+pad*2)
	copy(padded[pad:], samples)
	wavBytes := encodeWAV(padded, sampleRate)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wavBytes); err != nil {
		return "", err
	}
	_ = mw.WriteField("response_format", "json")
	_ = mw.WriteField("temperature", "0.0")
	if w.cfg.Language != "" {
		_ = mw.WriteField("language", w.cfg.Language)
	}
	prompt := "以下是简体中文与英文技术术语混合的语音，请输出简体中文并保留英文拼写："
	if w.glossary != "" {
		prompt += w.glossary
	} else {
		prompt += "Go语言, Java, Rust, Python, Docker, Wayland, KWin"
	}
	_ = mw.WriteField("prompt", prompt)
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, w.serverURL+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := w.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("whisper-server HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", err
	}
	return strings.TrimSpace(parsed.Text), nil
}
