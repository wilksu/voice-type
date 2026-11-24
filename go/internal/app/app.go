package app

import (
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
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

func ComputeRMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// HasSpeech returns true only when audio has enough active speech frames above
// typical mic noise/fan hum, preventing "嗯/可以" hallucinations on silence.
func HasSpeech(samples []float32, sampleRate int) bool {
	if ComputeRMS(samples) < 0.007 {
		return false
	}
	frameSize := sampleRate / 20 // 50ms
	if frameSize <= 0 || len(samples) < frameSize {
		return ComputeRMS(samples) >= 0.012
	}
	activeFrames := 0
	for i := 0; i+frameSize <= len(samples); i += frameSize {
		if ComputeRMS(samples[i:i+frameSize]) >= 0.012 {
			activeFrames++
			if activeFrames >= 2 {
				return true
			}
		}
	}
	return false
}

// FindPauseSplit looks for a natural >=350ms quiet pause after at least 2.0s of audio,
// returning a sample index inside the pause where the segment can be cleanly finalized.
func FindPauseSplit(samples []float32, sampleRate int) int {
	frameSize := sampleRate / 20 // 50ms frame (800 samples at 16kHz)
	minSegmentSamples := sampleRate * 2
	const pauseFramesNeeded = 7 // 350ms quiet pause
	const silenceRMS = 0.008

	if len(samples) < minSegmentSamples+pauseFramesNeeded*frameSize {
		return 0
	}

	quietRun := 0
	// Scan from 1.5s onward so we don't split tiny leading silence
	startFrame := (sampleRate * 3 / 2) / frameSize
	totalFrames := len(samples) / frameSize
	for f := startFrame; f < totalFrames; f++ {
		frame := samples[f*frameSize : (f+1)*frameSize]
		if ComputeRMS(frame) < silenceRMS {
			quietRun++
			if quietRun == pauseFramesNeeded {
				// Split in the middle of the quiet pause
				splitFrame := f - pauseFramesNeeded/2
				return splitFrame * frameSize
			}
		} else {
			quietRun = 0
		}
	}
	return 0
}

type VoiceTypeApp struct {
	cfgPath    string
	cfg        config.AppConfig
	recorder   *audio.PipeWireRecorder
	asr        asr.Recognizer
	previewAsr asr.Recognizer
	polisher   *polish.DoubaoPolisher
	injector   *injector.WaylandTextInjector
	notifier   *notify.StatusNotifier

	mu               sync.Mutex
	listener         *hotkey.EvdevListener
	tray             *tray.Tray
	enabled          atomic.Bool
	polishEnabled    atomic.Bool
	quitting         atomic.Bool
	recycling        atomic.Bool
	recording        bool
	reloadCh         chan struct{}
	sessionID        atomic.Int64
	previewStop      chan struct{}
	committedParts   []string
	committedSamples int
}

func initPreviewRecognizer(cfg config.AppConfig, mainAsr asr.Recognizer) asr.Recognizer {
	pe := strings.ToLower(strings.TrimSpace(cfg.Asr.PreviewEngine))
	if pe == "" || pe == "off" || !config.LookupEngine(pe).SupportsLivePreview {
		return nil
	}
	if pe == strings.ToLower(strings.TrimSpace(cfg.Asr.Engine)) &&
		cfg.Asr.PreviewConfig().ResolvedModelDir() == cfg.Asr.ResolvedModelDir() {
		return mainAsr
	}
	return asr.NewRecognizer(cfg.Asr.PreviewConfig(), cfg.Polish.Glossary)
}

func New(cfgPath string, cfg config.AppConfig) *VoiceTypeApp {
	logger.SetLevel(cfg.Logging.Level)
	mainAsr := asr.NewRecognizer(cfg.Asr, cfg.Polish.Glossary)
	app := &VoiceTypeApp{
		cfgPath:    cfgPath,
		cfg:        cfg,
		recorder:   audio.NewPipeWireRecorder(audio.SampleRate),
		asr:        mainAsr,
		previewAsr: initPreviewRecognizer(cfg, mainAsr),
		polisher:   polish.NewDoubaoPolisher(cfg.Polish),
		injector:   injector.NewWaylandTextInjector(cfg.Injector),
		notifier:   notify.NewStatusNotifier(cfg.Notify),
		reloadCh:   make(chan struct{}, 1),
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
	a.committedParts = nil
	a.committedSamples = 0
	notifier := a.notifier
	prevAsr := a.previewAsr
	err := a.recorder.Start()
	a.mu.Unlock()

	if err != nil {
		log.Printf("Failed to start recording: %v", err)
		notifier.OnError("录音失败", err.Error())
		return
	}
	a.mu.Lock()
	a.recording = true
	a.mu.Unlock()
	notifier.OnListeningStart()
	if prevAsr != nil {
		go a.previewLoop(sid, stopCh, prevAsr)
	}
}

func (a *VoiceTypeApp) previewLoop(sid int64, stopCh <-chan struct{}, prevAsr asr.Recognizer) {
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

		a.mu.Lock()
		offset := a.committedSamples
		a.mu.Unlock()

		seg, _ := a.recorder.SnapshotFrom(offset)
		if len(seg) >= audio.SampleRate/3 {
			// If a natural pause occurred after >=2s, finalize that segment once and advance offset
			if split := FindPauseSplit(seg, audio.SampleRate); split > 0 {
				if ComputeRMS(seg[:split]) >= 0.004 {
					if part, err := prevAsr.Transcribe(seg[:split], audio.SampleRate); err == nil && part != "" {
						a.mu.Lock()
						if sid == a.sessionID.Load() {
							a.committedParts = append(a.committedParts, part)
							a.committedSamples += split
						}
						a.mu.Unlock()
						seg = seg[split:]
					}
				} else {
					a.mu.Lock()
					if sid == a.sessionID.Load() {
						a.committedSamples += split
					}
					a.mu.Unlock()
					seg = seg[split:]
				}
			}

			// Preview only the active (uncommitted) segment, capped at last 8s
			previewSamples := seg
			maxPreview := audio.SampleRate * 8
			if len(previewSamples) > maxPreview {
				previewSamples = previewSamples[len(previewSamples)-maxPreview:]
			}

			var tailText string
			if len(previewSamples) >= audio.SampleRate/3 && ComputeRMS(previewSamples) >= 0.004 {
				if t, err := prevAsr.Transcribe(previewSamples, audio.SampleRate); err == nil {
					tailText = t
				}
			}

			a.mu.Lock()
			combined := strings.Join(a.committedParts, "") + tailText
			notifier := a.notifier
			a.mu.Unlock()

			if combined != "" && sid == a.sessionID.Load() {
				select {
				case <-stopCh:
					return
				default:
				}
				formatted := FormatPreviewText(combined, 24)
				if formatted != lastShown {
					lastShown = formatted
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
	a.recording = false
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
	asrRec := a.asr
	polisher := a.polisher
	inj := a.injector
	a.mu.Unlock()

	if !HasSpeech(samples, sampleRate) {
		notifier.OnDone("")
		return
	}

	notifier.OnProcessing()

	tAsr := time.Now()
	rawText, err := asrRec.Transcribe(samples, sampleRate)
	asrMs := time.Since(tAsr).Milliseconds()
	if err != nil {
		log.Printf("ASR failed: %v", err)
		notifier.OnError("识别失败", err.Error())
		return
	}
	rawText = strings.TrimSpace(rawText)

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
	a.maybeRecycleASR()
}

func currentRSSMB() int {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || pages <= 0 {
		return 0
	}
	return int((pages * int64(os.Getpagesize())) / (1024 * 1024))
}

func (a *VoiceTypeApp) maybeRecycleASR() {
	a.mu.Lock()
	maxMB := a.cfg.Asr.MaxRSSMB
	a.mu.Unlock()
	if maxMB <= 0 {
		return
	}
	beforeMB := currentRSSMB()
	if beforeMB <= maxMB {
		return
	}
	if !a.recycling.CompareAndSwap(false, true) {
		return
	}
	defer a.recycling.Store(false)

	a.mu.Lock()
	if a.recording || a.quitting.Load() || a.asr == nil {
		a.mu.Unlock()
		return
	}
	cfg := a.cfg
	oldAsr := a.asr
	oldPreview := a.previewAsr
	newAsr := asr.NewRecognizer(cfg.Asr, cfg.Polish.Glossary)
	newPreview := initPreviewRecognizer(cfg, newAsr)
	a.asr = newAsr
	a.previewAsr = newPreview
	a.mu.Unlock()

	oldAsr.Close()
	if oldPreview != nil && oldPreview != oldAsr {
		oldPreview.Close()
	}
	asr.TrimMemory()

	if err := newAsr.Load(); err != nil {
		log.Printf("Recycle ASR reload failed: %v", err)
	}
	if newPreview != nil && newPreview != newAsr {
		if err := newPreview.Load(); err != nil {
			log.Printf("Recycle preview ASR reload failed: %v", err)
		}
	}
	logger.Infof("Recycled ASR recognizer (RSS %d MB > %d MB limit) -> now %d MB", beforeMB, maxMB, currentRSSMB())
}

func (a *VoiceTypeApp) closeRecognizers() {
	a.mu.Lock()
	mainRec := a.asr
	prevRec := a.previewAsr
	a.asr = nil
	a.previewAsr = nil
	a.mu.Unlock()
	if mainRec != nil {
		mainRec.Close()
	}
	if prevRec != nil && prevRec != mainRec {
		prevRec.Close()
	}
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
	defer a.closeRecognizers()

	if a.previewAsr != nil && a.previewAsr != a.asr {
		if err := a.previewAsr.Load(); err != nil {
			log.Printf("Preview ASR load failed (disabling preview): %v", err)
			a.mu.Lock()
			a.previewAsr = nil
			a.mu.Unlock()
		}
	}

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
	oldEngine := a.cfg.Asr.Engine
	oldLang := a.cfg.Asr.Language
	oldModelDir := a.cfg.Asr.ModelDir
	oldPreviewEngine := a.cfg.Asr.PreviewEngine
	oldPreviewModelDir := a.cfg.Asr.PreviewModelDir
	a.cfg = cfg
	oldNotifier := a.notifier
	a.notifier = notify.NewStatusNotifier(cfg.Notify)
	a.polisher = polish.NewDoubaoPolisher(cfg.Polish)
	a.injector.UpdateConfig(cfg.Injector)
	var oldAsr asr.Recognizer
	if cfg.Asr.Engine != oldEngine || cfg.Asr.Language != oldLang || cfg.Asr.ModelDir != oldModelDir || cfg.Polish.Glossary != "" {
		if cfg.Asr.Engine != oldEngine || cfg.Asr.Language != oldLang || cfg.Asr.ModelDir != oldModelDir || cfg.Asr.Engine == "whisper" {
			oldAsr = a.asr
			a.asr = asr.NewRecognizer(cfg.Asr, cfg.Polish.Glossary)
		}
	}
	newAsr := a.asr
	var oldPreviewAsr asr.Recognizer
	if cfg.Asr.PreviewEngine != oldPreviewEngine || cfg.Asr.PreviewModelDir != oldPreviewModelDir || cfg.Asr.Language != oldLang || oldAsr != nil {
		if a.previewAsr != nil && a.previewAsr != oldAsr && a.previewAsr != newAsr {
			oldPreviewAsr = a.previewAsr
		}
		a.previewAsr = initPreviewRecognizer(cfg, newAsr)
	}
	newPreviewAsr := a.previewAsr
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
	if oldAsr != nil {
		oldAsr.Close()
		go func() { _ = newAsr.Load() }()
	}
	if oldPreviewAsr != nil {
		oldPreviewAsr.Close()
	}
	if newPreviewAsr != nil && newPreviewAsr != newAsr {
		go func() {
			if err := newPreviewAsr.Load(); err != nil {
				log.Printf("Reload preview ASR failed: %v", err)
			}
		}()
	}
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
