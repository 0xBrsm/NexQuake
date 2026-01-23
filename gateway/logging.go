package main

import (
	"log"
	"os"
	"strings"
)

type logLevel int

const (
	logError logLevel = iota
	logWarn
	logInfo
	logDebug
)

var currentLogLevel = logInfo

func initLogging() {
	level := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	switch level {
	case "", "info":
		currentLogLevel = logInfo
	case "debug":
		currentLogLevel = logDebug
	case "warn", "warning":
		currentLogLevel = logWarn
	case "error":
		currentLogLevel = logError
	default:
		currentLogLevel = logInfo
		log.Printf("Unknown LOG_LEVEL=%q (expected: error|warn|info|debug); defaulting to info", level)
	}
}

func logf(level logLevel, format string, args ...any) {
	if level <= currentLogLevel {
		log.Printf(format, args...)
	}
}

func errorf(format string, args ...any) { logf(logError, format, args...) }
func warnf(format string, args ...any)  { logf(logWarn, format, args...) }
func infof(format string, args ...any)  { logf(logInfo, format, args...) }
func debugf(format string, args ...any) { logf(logDebug, format, args...) }
