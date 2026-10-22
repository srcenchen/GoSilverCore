// Package sender provides a context-aware headless sender entry point.
package sender

import (
	"context"
	"go-silver-core/pkg/gosilver"
	"log/slog"
)

func Run(ctx context.Context, filePath, addr string) error {
	srv := gosilver.NewServer(addr, filePath)
	if err := srv.Start(); err != nil {
		return err
	}
	defer srv.Stop()
	<-ctx.Done()
	return ctx.Err()
}

// Start is retained for callers using the original blocking API.
func Start(filePath string) {
	if err := Run(context.Background(), filePath, ":0"); err != nil {
		slog.Error("sender stopped", "error", err)
	}
}
