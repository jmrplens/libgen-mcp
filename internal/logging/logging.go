// Package logging configura el logger estructurado del servidor sobre stderr.
//
// El canal stdout está reservado para el transporte JSON-RPC del MCP, por lo que
// todos los logs se emiten en formato JSON hacia os.Stderr mediante log/slog.
package logging

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"
)

// ParseLevel convierte una cadena en un slog.Level.
//
// Acepta "debug", "info", "warn", "warning" y "error" sin distinguir mayúsculas
// y recortando espacios. Una cadena vacía devuelve slog.LevelInfo. Cualquier otro
// valor produce un error.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errors.New("unknown log level: " + s)
	}
}

// Setup instala como logger por defecto un slog.JSONHandler sobre os.Stderr
// filtrado al nivel indicado.
func Setup(level slog.Level) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// ToolCall registra el resultado de la ejecución de una herramienta MCP.
//
// Emite un log de nivel Info cuando err es nil y de nivel Error en caso
// contrario, incluyendo siempre el nombre de la herramienta y la duración
// transcurrida desde start.
func ToolCall(tool string, start time.Time, err error) {
	duration := time.Since(start)
	if err != nil {
		slog.Error("tool call failed", "tool", tool, "duration", duration, "error", err)
		return
	}
	slog.Info("tool call completed", "tool", tool, "duration", duration)
}
