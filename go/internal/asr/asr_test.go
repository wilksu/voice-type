package asr

import "testing"

func TestCleanSenseVoiceText(t *testing.T) {
	raw := "<|zh|><|NEUTRAL|><|Speech|><|withitn|>今天测试一下 Python 和 Wayland。"
	got := CleanSenseVoiceText(raw)
	if got != "今天测试一下 Python 和 Wayland。" {
		t.Fatalf("unexpected cleaned text: %q", got)
	}
}
