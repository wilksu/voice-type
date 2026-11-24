package modeldl

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	SenseVoiceModelName = "sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17"
	FireRedModelName    = "sherpa-onnx-fire-red-asr-large-zh_en-2025-02-16"
	ParaformerModelName = "sherpa-onnx-paraformer-zh-2024-03-09"
	Qwen3ASRModelName   = "sherpa-onnx-qwen3-asr-0.6B-int8"
	WhisperModelDirName = "whisper"
	WhisperModelFile    = "ggml-large-v3-turbo-q5_0.bin"

	qwen3ModelScopeBase = "https://modelscope.cn/models/zengshuishui/Qwen3-ASR-onnx/resolve/master"
	qwen3HfMirrorBase   = "https://hf-mirror.com/csukuangfj2/sherpa-onnx-qwen3-asr-0.6B-int8-2026-03-25/resolve/main"

	senseVoiceModelScopeBase = "https://modelscope.cn/models/gomodels/sherpa/resolve/master/" + SenseVoiceModelName
	senseVoiceHfMirrorBase   = "https://hf-mirror.com/csukuangfj/" + SenseVoiceModelName + "/resolve/main"

	fireRedModelScopeBase = "https://modelscope.cn/models/csukuangfj/" + FireRedModelName + "/resolve/master"
	fireRedHfMirrorBase   = "https://hf-mirror.com/csukuangfj/" + FireRedModelName + "/resolve/main"

	paraformerHfMirrorBase = "https://hf-mirror.com/csukuangfj/" + ParaformerModelName + "/resolve/main"

	whisperModelScopeBase = "https://modelscope.cn/models/timeless/whispercpp/resolve/master"
	whisperHfMirrorBase   = "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main"
)

func DefaultModelsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "voice-type", "models")
}

func Download(engine, baseDir, source string, force bool) error {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "", "qwen3-asr":
		return downloadQwen3ASR(baseDir, source, force)
	case "firered":
		return downloadSet(baseDir, FireRedModelName, fireRedModelScopeBase, fireRedHfMirrorBase, source, force,
			"encoder.int8.onnx", "decoder.int8.onnx", "tokens.txt")
	case "sensevoice":
		return downloadSet(baseDir, SenseVoiceModelName, senseVoiceModelScopeBase, senseVoiceHfMirrorBase, source, force,
			"model.int8.onnx", "tokens.txt")
	case "paraformer":
		return downloadSet(baseDir, ParaformerModelName, paraformerHfMirrorBase, paraformerHfMirrorBase, source, force,
			"model.int8.onnx", "tokens.txt")
	case "whisper":
		return downloadSet(baseDir, WhisperModelDirName, whisperModelScopeBase, whisperHfMirrorBase, source, force,
			WhisperModelFile)
	default:
		return fmt.Errorf("unsupported download engine %q (supported: qwen3-asr, sensevoice, whisper, firered, paraformer)", engine)
	}
}

func downloadQwen3ASR(baseDir, source string, force bool) error {
	if baseDir == "" {
		baseDir = DefaultModelsDir()
	}
	targetDir := filepath.Join(baseDir, Qwen3ASRModelName)
	type filePair struct{ remote, local string }
	files := []filePair{
		{"model_0.6B/conv_frontend.onnx", "conv_frontend.onnx"},
		{"model_0.6B/encoder.int8.onnx", "encoder.int8.onnx"},
		{"model_0.6B/decoder.int8.onnx", "decoder.int8.onnx"},
		{"tokenizer/chat_template.json", "tokenizer/chat_template.json"},
		{"tokenizer/config.json", "tokenizer/config.json"},
		{"tokenizer/merges.txt", "tokenizer/merges.txt"},
		{"tokenizer/preprocessor_config.json", "tokenizer/preprocessor_config.json"},
		{"tokenizer/tokenizer_config.json", "tokenizer/tokenizer_config.json"},
		{"tokenizer/vocab.json", "tokenizer/vocab.json"},
	}
	baseURL := qwen3ModelScopeBase
	if source == "hf-mirror" {
		baseURL = qwen3HfMirrorBase
		for i := range files {
			files[i].remote = files[i].local
		}
	}
	for _, f := range files {
		dest := filepath.Join(targetDir, f.local)
		if !force {
			if _, err := os.Stat(dest); err == nil {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		url := baseURL + "/" + f.remote
		fmt.Printf("Downloading %s -> %s...\n", url, dest)
		if err := downloadFile(url, dest); err != nil {
			return err
		}
	}
	return nil
}

func DownloadSenseVoice(baseDir, source string, force bool) error {
	return Download("sensevoice", baseDir, source, force)
}

func downloadSet(baseDir, modelName, msBase, hfBase, source string, force bool, files ...string) error {
	if baseDir == "" {
		baseDir = DefaultModelsDir()
	}
	targetDir := filepath.Join(baseDir, modelName)

	allPresent := !force
	if allPresent {
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(targetDir, f)); err != nil {
				allPresent = false
				break
			}
		}
	}
	if allPresent {
		fmt.Printf("Model already present at %s\n", targetDir)
		return nil
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	baseURL := msBase
	if source == "hf-mirror" {
		baseURL = hfBase
	}

	for _, name := range files {
		dest := filepath.Join(targetDir, name)
		if !force {
			if _, err := os.Stat(dest); err == nil {
				continue
			}
		}
		if err := downloadFile(baseURL+"/"+name, dest); err != nil {
			return err
		}
	}
	fmt.Printf("Done: %s\n", targetDir)
	return nil
}

func downloadFile(url, dest string) error {
	fmt.Printf("Downloading %s -> %s\n", url, dest)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "voice-type/0.1")

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, url)
	}

	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}

	started := time.Now()
	lastReport := started
	var total int64
	buf := make([]byte, 256*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				_ = out.Close()
				return wErr
			}
			total += int64(n)
			now := time.Now()
			if now.Sub(lastReport) >= 2*time.Second {
				mb := float64(total) / (1024 * 1024)
				speed := mb / now.Sub(started).Seconds()
				if resp.ContentLength > 0 {
					pct := float64(total) * 100 / float64(resp.ContentLength)
					totalMB := float64(resp.ContentLength) / (1024 * 1024)
					fmt.Printf("  %s: %.1f/%.1f MB (%.1f%%, %.2f MB/s)\n", filepath.Base(dest), mb, totalMB, pct, speed)
				} else {
					fmt.Printf("  %s: %.1f MB (%.2f MB/s)\n", filepath.Base(dest), mb, speed)
				}
				lastReport = now
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = out.Close()
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(part, dest)
}
