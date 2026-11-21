package hotkey

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"

	"voice-type/internal/config"
)

const (
	evKey     = 0x01
	keyMax    = 0x2ff
	evIOCGBit = 0x20
)

var keyCodes = map[string]uint16{
	"KEY_LEFTCTRL":   29,
	"KEY_LEFTSHIFT":  42,
	"KEY_RIGHTSHIFT": 54,
	"KEY_LEFTALT":    56,
	"KEY_SPACE":      57,
	"KEY_CAPSLOCK":   58,
	"KEY_F1":         59,
	"KEY_F2":         60,
	"KEY_F3":         61,
	"KEY_F4":         62,
	"KEY_F5":         63,
	"KEY_F6":         64,
	"KEY_F7":         65,
	"KEY_F8":         66,
	"KEY_F9":         67,
	"KEY_F10":        68,
	"KEY_SCROLLLOCK": 70,
	"KEY_F11":        87,
	"KEY_F12":        88,
	"KEY_RIGHTCTRL":  97,
	"KEY_SYSRQ":      99,
	"KEY_RIGHTALT":   100,
	"KEY_HOME":       102,
	"KEY_PAGEUP":     104,
	"KEY_END":        107,
	"KEY_PAGEDOWN":   109,
	"KEY_INSERT":     110,
	"KEY_DELETE":     111,
	"KEY_PAUSE":      119,
	"KEY_LEFTMETA":   125,
	"KEY_RIGHTMETA":  126,
	"KEY_MENU":       127,
}

type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

func ResolveKeycode(name string) (uint16, error) {
	norm := strings.ToUpper(strings.TrimSpace(name))
	if !strings.HasPrefix(norm, "KEY_") {
		norm = "KEY_" + norm
	}
	code, ok := keyCodes[norm]
	if !ok {
		return 0, fmt.Errorf("unknown evdev key name: %q", name)
	}
	return code, nil
}

func ioctlEviocgbit(ev, length uintptr) uintptr {
	return (2 << 30) | (length << 16) | ('E' << 8) | (evIOCGBit + ev)
}

func deviceSupportsKey(fd int, target uint16) bool {
	var evBits [4]byte
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(fd),
		ioctlEviocgbit(0, uintptr(len(evBits))),
		uintptr(unsafe.Pointer(&evBits[0])),
	)
	if errno != 0 || (evBits[0]&(1<<evKey)) == 0 {
		return false
	}

	var keyBits [(keyMax + 7) / 8]byte
	_, _, errno = unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(fd),
		ioctlEviocgbit(evKey, uintptr(len(keyBits))),
		uintptr(unsafe.Pointer(&keyBits[0])),
	)
	if errno != 0 {
		return false
	}
	return (keyBits[target/8] & (1 << (target % 8))) != 0
}

func FindKeyboardDevices(target uint16) []*os.File {
	paths, _ := filepath.Glob("/dev/input/event*")
	var files []*os.File
	for _, p := range paths {
		f, err := os.OpenFile(p, os.O_RDONLY, 0)
		if err != nil {
			continue
		}
		if deviceSupportsKey(int(f.Fd()), target) {
			files = append(files, f)
		} else {
			_ = f.Close()
		}
	}
	return files
}

type EvdevListener struct {
	cfg        config.HotkeyConfig
	targetCode uint16
	onStart    func()
	onStop     func()

	mu       sync.Mutex
	isActive bool
	enabled  bool
	stopCh   chan struct{}
	files    []*os.File
}

func NewEvdevListener(cfg config.HotkeyConfig, onStart, onStop func()) (*EvdevListener, error) {
	code, err := ResolveKeycode(cfg.Key)
	if err != nil {
		return nil, err
	}
	return &EvdevListener{
		cfg:        cfg,
		targetCode: code,
		onStart:    onStart,
		onStop:     onStop,
		enabled:    true,
		stopCh:     make(chan struct{}),
	}, nil
}

func (l *EvdevListener) SetEnabled(enabled bool) {
	l.mu.Lock()
	l.enabled = enabled
	l.mu.Unlock()
}

func (l *EvdevListener) SetMode(mode string) {
	l.mu.Lock()
	l.cfg.Mode = mode
	l.isActive = false
	l.mu.Unlock()
}

func (l *EvdevListener) Run() error {
	files := FindKeyboardDevices(l.targetCode)
	if len(files) == 0 {
		return fmt.Errorf("no readable keyboard devices under /dev/input/event*")
	}

	l.mu.Lock()
	l.files = files
	l.mu.Unlock()

	log.Printf("Listening for %s (mode=%s) on %d device(s)", l.cfg.Key, l.cfg.Mode, len(files))

	for _, f := range files {
		_ = unix.SetNonblock(int(f.Fd()), true)
	}

	var wg sync.WaitGroup
	for _, f := range files {
		wg.Add(1)
		go func(dev *os.File) {
			defer wg.Done()
			l.readDevice(dev)
		}(f)
	}

	<-l.stopCh
	wg.Wait()
	for _, f := range files {
		_ = f.Close()
	}
	return nil
}

func (l *EvdevListener) Stop() {
	select {
	case <-l.stopCh:
	default:
		close(l.stopCh)
	}
}

func (l *EvdevListener) readDevice(dev *os.File) {
	var ev inputEvent
	buf := make([]byte, unsafe.Sizeof(ev))
	fds := []unix.PollFd{{Fd: int32(dev.Fd()), Events: unix.POLLIN}}

	for {
		select {
		case <-l.stopCh:
			return
		default:
		}

		n, err := unix.Poll(fds, 150)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}

		for {
			readN, readErr := unix.Read(int(dev.Fd()), buf)
			if readErr == unix.EAGAIN || readErr == unix.EWOULDBLOCK {
				break
			}
			if readErr != nil || readN < len(buf) {
				return
			}
			ev.Type = binary.LittleEndian.Uint16(buf[16:18])
			ev.Code = binary.LittleEndian.Uint16(buf[18:20])
			ev.Value = int32(binary.LittleEndian.Uint32(buf[20:24]))

			if ev.Type != evKey || ev.Code != l.targetCode {
				continue
			}
			l.handleKeyEvent(ev.Value)
		}
	}
}

func (l *EvdevListener) handleKeyEvent(value int32) {
	if value == 2 { // key repeat
		return
	}
	l.mu.Lock()
	if !l.enabled {
		l.mu.Unlock()
		return
	}

	if l.cfg.Mode == "toggle" {
		if value == 1 {
			if !l.isActive {
				l.isActive = true
				l.mu.Unlock()
				l.onStart()
				return
			}
			l.isActive = false
			l.mu.Unlock()
			l.onStop()
			return
		}
		l.mu.Unlock()
		return
	}

	// push_to_talk
	if value == 1 && !l.isActive {
		l.isActive = true
		l.mu.Unlock()
		l.onStart()
		return
	}
	if value == 0 && l.isActive {
		l.isActive = false
		l.mu.Unlock()
		l.onStop()
		return
	}
	l.mu.Unlock()
}
