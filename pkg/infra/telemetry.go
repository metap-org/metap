package infra

import (
	"log/slog"
	"os"

	"github.com/metap-org/metap/pkg/kit/env"

	"github.com/joho/godotenv"
)

// InitTracing sets up the default slog logger for every binary in this module — call this before
// anything else in main(), ahead of LoadConfig.
func InitTracing() {
	_ = godotenv.Load()

	level := slog.LevelInfo
	switch env.String("LOG_LEVEL", "") {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}
