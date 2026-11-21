package modeldl

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	ModelName      = "sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17"
	ModelScopeBase = "https://modelscope.cn/models/gomodels/sherpa/resolve/master/" + ModelName
	HfMirrorBase   = "https://hf-mirror.com/csukuangfj/" + ModelName + "/resolve/main"
)

func DefaultModelsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "voice-type", "models")
}

func DownloadSenseVoice(baseDir, source string, force bool) error {
	if baseDir == "" {
		baseDir = DefaultModelsDir()
	}
	targetDir := filepath.Join(baseDir, ModelName)
	modelPath := filepath.Join(targetDir, "model.int8.onnx")
	tokensPath := filepath.Join(targetDir, "tokens.txt")

	if !force {
		if _, err1 := os.Stat(modelPath); err1 == nil {
			if _, err2 := os.Stat(tokensPath); err2 == nil {
				fmt.Printf("Model already present at %s\n", targetDir)
				return nil
			}
		}
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	baseURL := ModelScopeBase
	if source == "hf-mirror" {
		baseURL = HfMirrorBase
	}

	for _, name := range []string{"model.int8.onnx", "tokens.txt"} {
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

	client := &http.Client{Timeout: 15 * time.Minute}
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
