package manager

import (
	"context"
	"log/slog"
	"sync"

	"github.com/skalluru/velocix/internal/config"
	gh "github.com/skalluru/velocix/internal/github"
	"github.com/skalluru/velocix/internal/poller"
	"github.com/skalluru/velocix/internal/store"
	"github.com/skalluru/velocix/internal/yacd"
)

type OrgInstance struct {
	Config  config.OrgConfig
	Client  *gh.Client
	Store   *store.Store
	Poller  *poller.Poller
	Scanner *yacd.Scanner
	Cancel  context.CancelFunc
}

type Manager struct {
	mu        sync.RWMutex
	cfg       *config.Config
	instances map[string]*OrgInstance
	activeOrg string
	logger    *slog.Logger
	parentCtx context.Context
	runner    *yacd.Runner
}

func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) *Manager {
	m := &Manager{
		cfg:       cfg,
		instances: make(map[string]*OrgInstance),
		activeOrg: cfg.ActiveOrg,
		logger:    logger,
		parentCtx: ctx,
		runner:    yacd.NewRunner(logger),
	}

	for _, orgCfg := range cfg.Orgs {
		m.startOrg(orgCfg)
	}

	// Apply persisted sync mode to all pollers
	manual := cfg.SyncMode == "manual"
	for _, inst := range m.instances {
		inst.Poller.SetMode(manual)
	}

	return m
}

func (m *Manager) startOrg(orgCfg config.OrgConfig) {
	ctx, cancel := context.WithCancel(m.parentCtx)
	client := gh.NewClient(orgCfg.GitHubToken, orgCfg.BaseURL, m.logger)
	st := store.NewStore(m.cfg.DataDir, orgCfg.Organization, m.logger)
	p := poller.New(client, st, orgCfg.Organization, m.cfg.PollInterval, m.logger)
	p.Start(ctx)

	scanner := yacd.NewScanner(client.HTTPClient(), client.GraphQLURL(), m.logger)

	m.instances[orgCfg.Name] = &OrgInstance{
		Config:  orgCfg,
		Client:  client,
		Store:   st,
		Poller:  p,
		Scanner: scanner,
		Cancel:  cancel,
	}
}

func (m *Manager) GetActiveStore() *store.Store {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.instances[m.activeOrg]; ok {
		return inst.Store
	}
	for _, inst := range m.instances {
		return inst.Store
	}
	return nil
}

func (m *Manager) GetActiveScanner() *yacd.Scanner {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.instances[m.activeOrg]; ok {
		return inst.Scanner
	}
	for _, inst := range m.instances {
		return inst.Scanner
	}
	return nil
}

func (m *Manager) GetActiveOrgConfig() *config.OrgConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.instances[m.activeOrg]; ok {
		return &inst.Config
	}
	return nil
}

func (m *Manager) Runner() *yacd.Runner {
	return m.runner
}

func (m *Manager) GetSyncMode() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg.SyncMode == "manual" {
		return "manual"
	}
	return "auto"
}

func (m *Manager) SetSyncMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mode != "auto" && mode != "manual" {
		mode = "auto"
	}
	m.cfg.SyncMode = mode
	m.cfg.Save()
	manual := mode == "manual"
	for _, inst := range m.instances {
		inst.Poller.SetMode(manual)
	}
}

func (m *Manager) GetRateLimit() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	per := map[string]gh.RateLimit{}
	var worst gh.RateLimit
	for _, inst := range m.instances {
		rl := inst.Client.GetRateLimit()
		per[inst.Config.Name] = rl
		// "Worst" = lowest remaining where limit is known
		if rl.Limit > 0 {
			if worst.Limit == 0 || rl.Remaining < worst.Remaining {
				worst = rl
			}
		}
	}
	return map[string]any{
		"worst":   worst,
		"per_org": per,
	}
}

func (m *Manager) TriggerSyncAll(ctx context.Context) {
	m.mu.RLock()
	pollers := make([]*poller.Poller, 0, len(m.instances))
	for _, inst := range m.instances {
		pollers = append(pollers, inst.Poller)
	}
	m.mu.RUnlock()

	for _, p := range pollers {
		go p.RunOnce(ctx)
	}
}

func (m *Manager) GetActiveOrg() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeOrg
}

func (m *Manager) SetActiveOrg(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.instances[name]; ok {
		m.activeOrg = name
		m.cfg.ActiveOrg = name
		m.cfg.Save()
		return true
	}
	for key, inst := range m.instances {
		if inst.Config.Organization == name {
			m.activeOrg = key
			m.cfg.ActiveOrg = key
			m.cfg.Save()
			return true
		}
	}
	return false
}

func (m *Manager) ListOrgs() []OrgInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var orgs []OrgInfo
	for _, inst := range m.instances {
		orgs = append(orgs, OrgInfo{
			Name:         inst.Config.Name,
			Organization: inst.Config.Organization,
			Active:       inst.Config.Name == m.activeOrg,
			RunCount:     len(inst.Store.GetAll()),
			BaseURL:      inst.Config.GitHubBaseURL(),
		})
	}
	return orgs
}

type OrgInfo struct {
	Name         string `json:"name"`
	Organization string `json:"organization"`
	Active       bool   `json:"active"`
	RunCount     int    `json:"run_count"`
	BaseURL      string `json:"base_url"`
}

func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range m.instances {
		inst.Cancel()
	}
}
