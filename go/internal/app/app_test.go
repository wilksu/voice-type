package app

import "testing"

func TestComputeRMS(t *testing.T) {
	if got := ComputeRMS(nil); got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}
	silence := make([]float32, 800)
	if got := ComputeRMS(silence); got != 0 {
		t.Fatalf("expected 0 for silence, got %v", got)
	}
	loud := make([]float32, 800)
	for i := range loud {
		loud[i] = 0.5
	}
	if got := ComputeRMS(loud); got < 0.49 || got > 0.51 {
		t.Fatalf("expected ~0.5, got %v", got)
	}
}

func TestHasSpeech(t *testing.T) {
	sr := 16000
	noise := make([]float32, sr)
	for i := range noise {
		noise[i] = 0.004
	}
	if HasSpeech(noise, sr) {
		t.Fatalf("expected low background noise to be rejected")
	}

	speech := make([]float32, sr)
	for i := 0; i < sr/4; i++ {
		speech[i] = 0.04
	}
	if !HasSpeech(speech, sr) {
		t.Fatalf("expected real speech frames to be accepted")
	}
}

func TestFindPauseSplit(t *testing.T) {
	sr := 16000
	// 2.0s speech + 0.5s silence + 0.5s speech
	samples := make([]float32, sr*3)
	for i := 0; i < sr*2; i++ {
		samples[i] = 0.1
	}
	for i := sr*2 + sr/2; i < sr*3; i++ {
		samples[i] = 0.1
	}

	split := FindPauseSplit(samples, sr)
	if split < sr*2 || split > sr*2+sr/2 {
		t.Fatalf("expected split inside pause [%d, %d], got %d", sr*2, sr*2+sr/2, split)
	}
}
