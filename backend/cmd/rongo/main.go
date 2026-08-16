// Command rongo serves the API and the embedded SPA.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/trick77/rongo/internal/httpapi"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	srv := httpapi.NewServer(httpapi.Deps{})

	addr := "127.0.0.1:8080"
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
