// Package obs wires logging.
//
// Two rules with teeth, from docs/12-security.md:
//
//   - `network` is a mandatory attribute on every money-path line. A log that does not say which
//     network it concerns is unusable during an isolation incident.
//   - An amount is logged through its string form, never as an integer or a float. A log is the
//     last place a float sneaks back in.
package obs

import (
	"log/slog"
	"os"
	"strings"
)

func Setup(level, service string) {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv})
	slog.SetDefault(slog.New(h).With("service", service))
}
