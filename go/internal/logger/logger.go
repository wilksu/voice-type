package logger

import (
	"log"
	"strings"
	"sync/atomic"
)

const (
	LevelError int32 = iota
	LevelWarn
	LevelInfo
	LevelDebug
)

var currentLevel atomic.Int32

func init() {
	currentLevel.Store(LevelInfo)
}

func ParseLevel(s string) int32 {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error":
		return LevelError
	case "warn", "warning":
		return LevelWarn
	case "debug":
		return LevelDebug
	default:
		return LevelInfo
	}
}

func SetLevel(s string) {
	currentLevel.Store(ParseLevel(s))
}

func Errorf(format string, args ...any) {
	if currentLevel.Load() >= LevelError {
		log.Printf("[ERROR] "+format, args...)
	}
}

func Warnf(format string, args ...any) {
	if currentLevel.Load() >= LevelWarn {
		log.Printf("[WARN] "+format, args...)
	}
}

func Infof(format string, args ...any) {
	if currentLevel.Load() >= LevelInfo {
		log.Printf("[INFO] "+format, args...)
	}
}

func Debugf(format string, args ...any) {
	if currentLevel.Load() >= LevelDebug {
		log.Printf("[DEBUG] "+format, args...)
	}
}
