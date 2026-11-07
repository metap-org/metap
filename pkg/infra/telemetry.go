package infra

import (
	"log/slog"
	"os"

	"github.com/joho/godotenv"
)

// InitTracing sets up the default slog logger for every binary in this module — call this
// before anything else in main(), ahead of LoadConfig. Reads LOG_LEVEL (the Go-idiomatic
// equivalent of the Rust RUST_LOG, which has no direct Go analog — EnvFilter's
// target-scoped-level syntax isn't something slog supports), defaulting to "info".
func InitTracing() {
	_ = godotenv.Load()

	level := slog.LevelInfo
	switch os.Getenv("LOG_LEVEL") {
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
