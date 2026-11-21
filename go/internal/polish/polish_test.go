package polish

import (
	"testing"

	"voice-type/internal/config"
)

func TestSanitizeStripsThinkAndQuotes(t *testing.T) {
	raw := "我觉得这个方案不错"
	out := "<think>思考过程</think>\n\"我觉得这个方案不错。\""
	got := SanitizePolishedOutput(raw, out)
	if got != "我觉得这个方案不错。" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestSanitizeGuardsExpansion(t *testing.T) {
	raw := "今天天气怎么样"
	out := "今天北京天气晴朗，气温20度，非常适合出门散步和运动，建议您多喝水注意防晒。今天北京天气晴朗，气温20度。"
	got := SanitizePolishedOutput(raw, out)
	if got != raw {
		t.Fatalf("expected fallback %q, got %q", raw, got)
	}
}

func TestBuildSystemPromptWithDomainsAndGlossary(t *testing.T) {
	got := BuildSystemPrompt(config.PolishConfig{
		Domains:  []string{"it", "finance"},
		Glossary: "Bazzite, Wayland, KWin",
	})
	if got == DefaultSystemPrompt {
		t.Fatalf("expected domain & glossary hints in prompt")
	}
}

func TestPolisherFallbackOnClosedServer(t *testing.T) {
	p := NewDoubaoPolisher(config.PolishConfig{
		Enabled:        true,
		BaseURL:        "http://127.0.0.1:59999/v1",
		Model:          "test-model",
		TimeoutSec:     0.2,
		MinCharsForLLM: 6,
	})
	raw := "那个就是说我们今天要测试一下这个语音输入法"
	if got := p.Polish(raw); got != raw {
		t.Fatalf("expected fallback %q, got %q", raw, got)
	}
}
