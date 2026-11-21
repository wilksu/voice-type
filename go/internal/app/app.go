package app

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"voice-type/internal/asr"
	"voice-type/internal/audio"
	"voice-type/internal/config"
	"voice-type/internal/hotkey"
	"voice-type/internal/injector"
	"voice-type/internal/logger"
	"voice-type/internal/notify"
	"voice-type/internal/polish"
	"voice-type/internal/settingsweb"
	"voice-type/internal/tray"
)

func FormatPreviewText(text string, maxRunes int) string {
	cleaned := strings.Join(strings.Fields(text), " ")
	runes := []rune(cleaned)
	if len(runes) <= maxRunes {
		return cleaned
	}
	return "…" + string(runes[len(runes)-(maxRunes-1):])
}

type VoiceTypeApp struct {
	cfgPath  string
	cfg      config.AppConfig
	recorder *audio.PipeWireRecorder
	asr      *asr.SenseVoiceASR
	polisher *polish.DoubaoPolisher
	injector *injector.WaylandTextInjector
	notifier *notify.StatusNotifier

	mu            sync.Mutex
	listener      *hotkey.EvdevListener
	tray          *tray.Tray
	enabled       atomic.Bool
	polishEnabled atomic.Bool
	quitting      atomic.Bool
	reloadCh      chan struct{}
	sessionID     atomic.Int64
	previewStop   chan struct{}
}

func New(cfgPath string, cfg config.AppConfig) *VoiceTypeApp {
	logger.SetLevel(cfg.Logging.Level)
	app := &VoiceTypeApp{
		cfgPath:  cfgPath,
		cfg:      cfg,
		recorder: audio.NewPipeWireRecorder(audio.SampleRate),
		asr:      asr.NewSenseVoiceASR(cfg.Asr),
		polisher: polish.NewDoubaoPolisher(cfg.Polish),
		injector: injector.NewWaylandTextInjector(cfg.Injector),
		notifier: notify.NewStatusNotifier(cfg.Notify),
		reloadCh: make(chan struct{}, 1),
	}
	app.enabled.Store(true)
	app.polishEnabled.Store(cfg.Polish.Enabled)
	return app
}

func (a *VoiceTypeApp) onStart() {
	a.mu.Lock()
	sid := a.sessionID.Add(1)
	stopCh := make(chan struct{})
	a.previewStop = stopCh
	notifier := a.notifier
	err := a.recorder.Start()
	a.mu.Unlock()

	if err != nil {
		log.Printf("Failed to start recording: %v", err)
		notifier.OnError("录音失败", err.Error())
		return
	}
	notifier.OnListeningStart()
	go a.previewLoop(sid, stopCh)
}

func (a *VoiceTypeApp) previewLoop(sid int64, stopCh <-chan struct{}) {
	lastShown := ""
	select {
	case <-stopCh:
		return
	case <-time.After(450 * time.Millisecond):
	}

	ticker := time.NewTicker(450 * time.Millisecond)
	defer ticker.Stop()

	for {
		if sid != a.sessionID.Load() {
			return
		}
		snap := a.recorder.Snapshot()
		if snap != nil && snap.DurationSec >= 0.35 {
			text, err := a.asr.Transcribe(snap.Samples, snap.SampleRate)
			if err == nil && text != "" && sid == a.sessionID.Load() {
				select {
				case <-stopCh:
					return
				default:
				}
				formatted := FormatPreviewText(text, 24)
				if formatted != lastShown {
					lastShown = formatted
					a.mu.Lock()
					notifier := a.notifier
					a.mu.Unlock()
					notifier.OnListeningPreview(formatted)
				}
			}
		}
		select {
		case <-stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (a *VoiceTypeApp) onStop() {
	a.mu.Lock()
	if a.previewStop != nil {
		close(a.previewStop)
		a.previewStop = nil
	}
	rec := a.recorder.Stop()
	minDur := a.cfg.Hotkey.MinDurationSec
	notifier := a.notifier
	a.mu.Unlock()

	if rec == nil || rec.DurationSec < minDur {
		notifier.OnDone("")
		return
	}

	go a.processAudio(rec.Samples, rec.SampleRate, rec.DurationSec)
}

func (a *VoiceTypeApp) processAudio(samples []float32, sampleRate int, durationSec float64) {
	t0 := time.Now()
	a.mu.Lock()
	notifier := a.notifier
	polisher := a.polisher
	inj := a.injector
	a.mu.Unlock()

	notifier.OnProcessing()

	tAsr := time.Now()
	rawText, err := a.asr.Transcribe(samples, sampleRate)
	asrMs := time.Since(tAsr).Milliseconds()
	if err != nil {
		log.Printf("ASR failed: %v", err)
		notifier.OnError("识别失败", err.Error())
		return
	}
	if rawText == "" {
		notifier.OnDone("")
		return
	}

	finalText := rawText
	var polishMs int64
	if a.polishEnabled.Load() {
		tPolish := time.Now()
		finalText = polisher.Polish(rawText)
		polishMs = time.Since(tPolish).Milliseconds()
	}

	if err := inj.Inject(finalText); err != nil {
		log.Printf("Inject failed: %v", err)
		notifier.OnError("上屏失败", err.Error())
		return
	}

	totalMs := time.Since(t0).Milliseconds()
	a.mu.Lock()
	logTranscript := a.cfg.Logging.LogTranscript
	a.mu.Unlock()
	if logTranscript {
		logger.Debugf("[%dms = ASR %dms + Polish %dms] %q -> %q", totalMs, asrMs, polishMs, rawText, finalText)
	} else {
		logger.Infof("[%dms = ASR %dms + Polish %dms] len=%d->%d", totalMs, asrMs, polishMs, len([]rune(rawText)), len([]rune(finalText)))
	}
	notifier.OnDone(FormatPreviewText(finalText, 36))
}

func (a *VoiceTypeApp) Run() error {
	if missing := a.injector.CheckDependencies(); len(missing) > 0 {
		return fmt.Errorf("missing system tools: %s", strings.Join(missing, ", "))
	}
	if err := a.injector.EnsureYdotoold(); err != nil {
		return err
	}
	defer a.injector.Cleanup()

	if err := a.asr.Load(); err != nil {
		return err
	}
	defer a.asr.Close()

	if a.polishEnabled.Load() {
		a.polisher.ResolveModel()
	}

	tr, err := tray.Start(true, a.polishEnabled.Load(), a.cfg.Hotkey.Key, tray.Callbacks{
		OnToggleEnabled: func(enabled bool) {
			a.enabled.Store(enabled)
			a.mu.Lock()
			l := a.listener
			a.mu.Unlock()
			if l != nil {
				l.SetEnabled(enabled)
			}
			log.Printf("Voice input enabled=%v", enabled)
		},
		OnTogglePolish: func(enabled bool) {
			a.polishEnabled.Store(enabled)
			log.Printf("AI polish enabled=%v", enabled)
		},
		OnOpenSettings: func() {
			settingsweb.Open(a.cfgPath, a.Reload)
		},
		OnQuit: func() {
			a.Stop()
		},
	})
	if err != nil {
		log.Printf("System tray disabled: %v", err)
	} else {
		a.tray = tr
		defer tr.Close()
	}

	defer a.notifier.Close()
	a.notifier.OnReady(a.cfg.Hotkey.Key)

	for {
		a.mu.Lock()
		hkCfg := a.cfg.Hotkey
		a.mu.Unlock()

		listener, err := hotkey.NewEvdevListener(hkCfg, a.onStart, a.onStop)
		if err != nil {
			return err
		}
		listener.SetEnabled(a.enabled.Load())

		a.mu.Lock()
		a.listener = listener
		a.mu.Unlock()

		runErr := listener.Run()
		if a.quitting.Load() {
			return nil
		}
		select {
		case <-a.reloadCh:
			continue
		default:
			if runErr != nil {
				return runErr
			}
			return nil
		}
	}
}

func (a *VoiceTypeApp) Reload() {
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		log.Printf("Reload config failed: %v", err)
		return
	}
	a.mu.Lock()
	oldKey := a.cfg.Hotkey.Key
	a.cfg = cfg
	oldNotifier := a.notifier
	a.notifier = notify.NewStatusNotifier(cfg.Notify)
	a.polisher = polish.NewDoubaoPolisher(cfg.Polish)
	a.injector.UpdateConfig(cfg.Injector)
	oldListener := a.listener
	if oldListener != nil {
		if cfg.Hotkey.Key != oldKey {
			select {
			case a.reloadCh <- struct{}{}:
			default:
			}
			oldListener.Stop()
		} else {
			oldListener.SetMode(cfg.Hotkey.Mode)
		}
	}
	tr := a.tray
	pol := a.polisher
	a.mu.Unlock()

	oldNotifier.Close()
	logger.SetLevel(cfg.Logging.Level)
	a.polishEnabled.Store(cfg.Polish.Enabled)
	if cfg.Polish.Enabled {
		go pol.ResolveModel()
	}
	if tr != nil {
		tr.UpdateState(a.enabled.Load(), cfg.Polish.Enabled, cfg.Hotkey.Key)
	}
	log.Printf("Reloaded config from %s", a.cfgPath)
}

func OpenSettingsWindow(cfgPath string) {
	settingsweb.Open(cfgPath, nil)
}

func (a *VoiceTypeApp) Stop() {
	a.quitting.Store(true)
	a.mu.Lock()
	if a.previewStop != nil {
		close(a.previewStop)
		a.previewStop = nil
	}
	if a.listener != nil {
		a.listener.Stop()
	}
	a.mu.Unlock()
}
