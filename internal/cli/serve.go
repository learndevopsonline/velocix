package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skalluru/velocix/internal/config"
	"github.com/skalluru/velocix/internal/manager"
	"github.com/skalluru/velocix/internal/web"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the web dashboard",
	RunE:  runServe,
}

var (
	servePort     int
	servePollSecs int
)

func init() {
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 0, "Web server port (default: from config or 8080)")
	serveCmd.Flags().IntVar(&servePollSecs, "poll-interval", 0, "Poll interval in seconds (default: from config or 30)")
}

func runServe(cmd *cobra.Command, args []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	if servePort != 0 {
		cfg.WebPort = servePort
	}
	if servePollSecs != 0 {
		cfg.PollInterval = time.Duration(servePollSecs) * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := manager.New(ctx, cfg, logger)
	defer mgr.Stop()

	srv := web.NewServer(mgr, cfg.WebPort, logger)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		logger.Info("shutting down...")
		cancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		srv.Shutdown(shutCtx)
	}()

	logger.Info("starting web server", "port", cfg.WebPort, "orgs", len(cfg.Orgs))
	fmt.Fprintf(os.Stderr, "Velocix dashboard: http://localhost:%d\n", cfg.WebPort)

	writePidFile(cfg.DataDir, os.Args)
	defer removePidFile(cfg.DataDir)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
