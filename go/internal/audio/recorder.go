package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const SampleRate = 16000

type RecordedAudio struct {
	Samples     []float32
	SampleRate  int
	DurationSec float64
}

type PipeWireRecorder struct {
	sampleRate int
	mu         sync.Mutex
	cmd        *exec.Cmd
	buf        []byte
	startedAt  time.Time
	doneCh     chan struct{}
}

func NewPipeWireRecorder(sampleRate int) *PipeWireRecorder {
	if sampleRate <= 0 {
		sampleRate = SampleRate
	}
	return &PipeWireRecorder{sampleRate: sampleRate}
}

func (r *PipeWireRecorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cmd != nil {
		return nil
	}
	if _, err := exec.LookPath("pw-record"); err != nil {
		return fmt.Errorf("pw-record not found: %w", err)
	}

	cmd := exec.Command(
		"pw-record",
		fmt.Sprintf("--rate=%d", r.sampleRate),
		"--channels=1",
		"--format=s16",
		"--raw",
		"-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	r.cmd = cmd
	r.buf = r.buf[:0]
	r.startedAt = time.Now()
	doneCh := make(chan struct{})
	r.doneCh = doneCh

	go r.readLoop(stdout, doneCh)
	return nil
}

func (r *PipeWireRecorder) readLoop(stdout io.ReadCloser, doneCh chan struct{}) {
	defer close(doneCh)
	tmp := make([]byte, 1600) // 50ms of 16kHz s16 mono
	for {
		n, err := stdout.Read(tmp)
		if n > 0 {
			r.mu.Lock()
			r.buf = append(r.buf, tmp[:n]...)
			r.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (r *PipeWireRecorder) bytesToAudio(raw []byte, fallback time.Duration) *RecordedAudio {
	usable := len(raw) - (len(raw) % 2)
	if usable <= 0 {
		return nil
	}
	count := usable / 2
	samples := make([]float32, count)
	for i := 0; i < count; i++ {
		s := int16(binary.LittleEndian.Uint16(raw[i*2 : i*2+2]))
		samples[i] = float32(s) / 32768.0
	}
	dur := float64(count) / float64(r.sampleRate)
	if dur <= 0 {
		dur = fallback.Seconds()
	}
	return &RecordedAudio{
		Samples:     samples,
		SampleRate:  r.sampleRate,
		DurationSec: dur,
	}
}

func (r *PipeWireRecorder) Snapshot() *RecordedAudio {
	r.mu.Lock()
	if len(r.buf) < 2 {
		r.mu.Unlock()
		return nil
	}
	raw := make([]byte, len(r.buf))
	copy(raw, r.buf)
	elapsed := time.Since(r.startedAt)
	r.mu.Unlock()

	return r.bytesToAudio(raw, elapsed)
}

func (r *PipeWireRecorder) Stop() *RecordedAudio {
	r.mu.Lock()
	cmd := r.cmd
	doneCh := r.doneCh
	startedAt := r.startedAt
	r.cmd = nil
	r.doneCh = nil
	r.mu.Unlock()

	if cmd == nil {
		return nil
	}

	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGINT)
		waitDone := make(chan error, 1)
		go func() { waitDone <- cmd.Wait() }()
		select {
		case <-waitDone:
		case <-time.After(1500 * time.Millisecond):
			_ = cmd.Process.Kill()
			<-waitDone
		}
	}
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(time.Second):
		}
	}

	r.mu.Lock()
	raw := make([]byte, len(r.buf))
	copy(raw, r.buf)
	r.buf = r.buf[:0]
	r.mu.Unlock()

	return r.bytesToAudio(raw, time.Since(startedAt))
}
