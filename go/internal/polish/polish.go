package polish

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"voice-type/internal/config"
	"voice-type/internal/logger"
)

const DefaultSystemPrompt = "你是语音输入法（ASR）文本润色与纠错引擎。请结合上下文语义：\n" +
	"1. 纠正语音识别同音错别字与中英谐音误识（如‘promote→Prompt’、‘够语言→Go语言’、‘头ken→token’、‘鱼装→预装’）；\n" +
	"2. 去除“那个、嗯、啊、就是、然后”等口癖、卡顿和整句重复（若连续重复同一句话只保留最后一遍）；\n" +
	"3. 理顺长句语序并补全恰当标点。\n" +
	"严禁回答问题、严禁扩写或解释、严禁加引号或 Markdown。只输出润色后的纯文本。"

var DomainHints = map[string]string{
	"it":      "IT/软件开发（重点修正编程与系统术语同音/谐音，如 Go语言/Rust/Python/Docker/Dock/token/systemd/Wayland/API/依赖/预装/拟物/网页/容器/镜像）",
	"finance": "金融/财经（重点修正量化、回撤、估值、年化、K线、ETF、流动性、套利、风控等专业术语）",
	"office":  "日常办公/沟通（重点理顺长句表达、修正高频同音字如的/地/得、在/再、做/作、预装/弹通知）",
	"science": "学术科研（重点保留英文专业词汇、公式概念与严谨书面语序）",
	"medical": "医学/健康（重点修正疾病、药品、检查指标等医学名词同音字）",
	"legal":   "法律/合规（重点保持法条、合同、权责条款的严谨用词）",
}

func BuildSystemPrompt(cfg config.PolishConfig) string {
	base := strings.TrimSpace(cfg.SystemPrompt)
	if base == "" {
		base = DefaultSystemPrompt
	}

	var extras []string
	if len(cfg.Domains) > 0 {
		var hints []string
		for _, d := range cfg.Domains {
			if h, ok := DomainHints[d]; ok {
				hints = append(hints, h)
			} else if strings.TrimSpace(d) != "" {
				hints = append(hints, d)
			}
		}
		if len(hints) > 0 {
			extras = append(extras, "当前偏好领域："+strings.Join(hints, "；")+"。")
		}
	}
	if g := strings.TrimSpace(cfg.Glossary); g != "" {
		extras = append(extras, "优先保留/纠正为以下常用专有名词："+g+"。")
	}

	if len(extras) == 0 {
		return base
	}
	return base + "\n" + strings.Join(extras, "\n")
}

var thinkTagRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

func SanitizePolishedOutput(rawText, outputText string) string {
	cleaned := strings.TrimSpace(thinkTagRe.ReplaceAllString(outputText, ""))
	if cleaned == "" {
		return rawText
	}
	runes := []rune(cleaned)
	if len(runes) >= 2 {
		if (runes[0] == '"' && runes[len(runes)-1] == '"' && !strings.ContainsRune(rawText, '"')) ||
			(runes[0] == '“' && runes[len(runes)-1] == '”' && !strings.ContainsRune(rawText, '“')) {
			cleaned = strings.TrimSpace(string(runes[1 : len(runes)-1]))
		}
	}
	if cleaned == "" {
		return rawText
	}
	maxAllowed := len([]rune(rawText))*2 + 25
	if maxAllowed < 40 {
		maxAllowed = 40
	}
	if len([]rune(cleaned)) > maxAllowed {
		logger.Warnf("Polished output looks expanded (%d -> %d chars); falling back to raw ASR.", len([]rune(rawText)), len([]rune(cleaned)))
		return rawText
	}
	return cleaned
}

type DoubaoPolisher struct {
	cfg           config.PolishConfig
	client        *http.Client
	mu            sync.Mutex
	resolvedModel string
}

func NewDoubaoPolisher(cfg config.PolishConfig) *DoubaoPolisher {
	timeout := time.Duration(cfg.TimeoutSec * float64(time.Second))
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	return &DoubaoPolisher{
		cfg:           cfg,
		resolvedModel: cfg.Model,
		client:        &http.Client{Timeout: timeout},
	}
}

func (p *DoubaoPolisher) baseURL() string {
	return strings.TrimRight(p.cfg.BaseURL, "/")
}

func (p *DoubaoPolisher) ResolveModel() string {
	p.mu.Lock()
	if p.resolvedModel != "" {
		m := p.resolvedModel
		p.mu.Unlock()
		return m
	}
	p.mu.Unlock()

	req, err := http.NewRequest(http.MethodGet, p.baseURL()+"/models", nil)
	if err != nil {
		return ""
	}
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return ""
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return ""
	}
	for _, item := range parsed.Data {
		if item.ID != "" && !strings.Contains(strings.ToLower(item.ID), "embed") {
			p.mu.Lock()
			p.resolvedModel = item.ID
			p.mu.Unlock()
			logger.Debugf("Auto-detected local LLM model: %s", item.ID)
			return item.ID
		}
	}
	return ""
}

func (p *DoubaoPolisher) Polish(text string) string {
	raw := strings.TrimSpace(text)
	if raw == "" || !p.cfg.Enabled {
		return raw
	}
	if len([]rune(raw)) < p.cfg.MinCharsForLLM {
		return raw
	}

	model := p.ResolveModel()
	if model == "" {
		return raw
	}

	sysPrompt := BuildSystemPrompt(p.cfg)

	maxTokens := len([]rune(raw))*4 + 512
	if maxTokens < 768 {
		maxTokens = 768
	}
	if maxTokens > 2048 {
		maxTokens = 2048
	}

	payload := map[string]any{
		"model":       model,
		"temperature": p.cfg.Temperature,
		"max_tokens":  maxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": "润色以下文本（只输出结果）：\n" + raw},
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, p.baseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return raw
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		logger.Warnf("LLM polish failed (%v); falling back to raw ASR.", err)
		return raw
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		logger.Warnf("LLM polish HTTP %d; falling back to raw ASR.", resp.StatusCode)
		return raw
	}

	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil || len(parsed.Choices) == 0 {
		return raw
	}
	content := parsed.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		logger.Warnf("LLM polish returned empty content (finish_reason=%s); falling back to raw ASR.", parsed.Choices[0].FinishReason)
		return raw
	}
	return SanitizePolishedOutput(raw, content)
}
