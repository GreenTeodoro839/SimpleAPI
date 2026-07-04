// Package logging configures the application-wide logrus logger.
package logging

import (
	"strings"

	"github.com/sirupsen/logrus"
)

// NewLogger builds a logrus logger at the given level. JSON output is used when
// jsonFmt is true; otherwise a readable text formatter is used.
func NewLogger(level string, jsonFmt bool) *logrus.Logger {
	l := logrus.New()
	if jsonFmt {
		l.SetFormatter(&logrus.JSONFormatter{})
	} else {
		l.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	}
	switch strings.ToLower(level) {
	case "debug":
		l.SetLevel(logrus.DebugLevel)
	case "warn", "warning":
		l.SetLevel(logrus.WarnLevel)
	case "error":
		l.SetLevel(logrus.ErrorLevel)
	default:
		l.SetLevel(logrus.InfoLevel)
	}
	return l
}
