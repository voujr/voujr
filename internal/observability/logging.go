package observability

import (
	"io"
	"log/slog"
	"strings"
)

// NewLogger builds a structured (text) logger at the given level, writing to w.
// In interactive TUI mode the caller passes a log file (not stderr) so logs don't
// corrupt the alternate-screen display; one-shot commands pass os.Stderr.
func NewLogger(level string, w io.Writer) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv}))
}
