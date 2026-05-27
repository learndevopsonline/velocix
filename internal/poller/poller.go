package poller

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	gh "github.com/skalluru/velocix/internal/github"
	"github.com/skalluru/velocix/internal/store"
)

type Poller struct {
	client   *gh.Client
	store    *store.Store
	org      string
	interval time.Duration
	logger   *slog.Logger

	// 0 = auto, 1 = manual
	mode atomic.Int32
}

func New(client *gh.Client, store *store.Store, org string, interval time.Duration, logger *slog.Logger) *Poller {
	return &Poller{
		client:   client,
		store:    store,
		org:      org,
		interval: interval,
		logger:   logger,
	}
}

func (p *Poller) SetMode(manual bool) {
	if manual {
		p.mode.Store(1)
	} else {
		p.mode.Store(0)
	}
}

func (p *Poller) IsManual() bool {
	return p.mode.Load() == 1
}

func (p *Poller) Start(ctx context.Context) {
	go func() {
		// Initial fetch immediately (even in manual mode, do one fetch to populate UI)
		p.poll(ctx)

		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				p.logger.Info("poller stopped")
				return
			case <-ticker.C:
				if p.IsManual() {
					continue // skip automatic polling in manual mode
				}
				p.poll(ctx)
			}
		}
	}()
}

func (p *Poller) RunOnce(ctx context.Context) error {
	return p.poll(ctx)
}

func (p *Poller) poll(ctx context.Context) error {
	p.logger.Info("polling workflow runs", "org", p.org)
	runs, err := p.client.FetchAllWorkflowRuns(ctx, p.org)
	if err != nil {
		p.logger.Error("poll failed", "error", err)
		return err
	}
	p.store.Update(runs)
	p.logger.Info("poll complete", "runs", len(runs))
	return nil
}
