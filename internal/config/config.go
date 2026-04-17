package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type OrgConfig struct {
	Name         string `yaml:"name" json:"name"`
	GitHubToken  string `yaml:"github_token" json:"-"`
	Organization string `yaml:"organization" json:"organization"`
	BaseURL      string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
}

func (o *OrgConfig) GitHubBaseURL() string {
	if o.BaseURL != "" {
		return o.BaseURL
	}
	return "https://github.com"
}

func (o *OrgConfig) GitHubAPIURL() string {
	if o.BaseURL != "" {
		return o.BaseURL + "/api/v3"
	}
	return ""
}

type Config struct {
	Orgs         []OrgConfig   `yaml:"orgs"`
	ActiveOrg    string        `yaml:"active_org"`
	PollInterval time.Duration `yaml:"poll_interval"`
	WebPort      int           `yaml:"web_port"`
	DataDir      string        `yaml:"data_dir"`

	// Legacy single-org fields (still supported for backward compat)
	GitHubToken  string `yaml:"github_token,omitempty"`
	Organization string `yaml:"organization,omitempty"`
}

func DefaultConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".velocix")
}

func DefaultConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "config.yaml")
}

func DefaultConfig() *Config {
	return &Config{
		PollInterval: 30 * time.Second,
		WebPort:      8080,
		DataDir:      DefaultConfigDir(),
	}
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(DefaultConfigPath())
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}
	}

	// Environment variables override
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		cfg.GitHubToken = token
	}
	if org := os.Getenv("VELOCIX_ORG"); org != "" {
		cfg.Organization = org
	}

	if cfg.DataDir == "" {
		cfg.DataDir = DefaultConfigDir()
	}

	// Migrate legacy single-org config into Orgs list
	cfg.migrateFromLegacy()

	return cfg, nil
}

func (c *Config) migrateFromLegacy() {
	if c.GitHubToken != "" && c.Organization != "" {
		found := false
		for _, o := range c.Orgs {
			if o.Organization == c.Organization {
				found = true
				break
			}
		}
		if !found {
			c.Orgs = append(c.Orgs, OrgConfig{
				Name:         c.Organization,
				GitHubToken:  c.GitHubToken,
				Organization: c.Organization,
			})
		}
		if c.ActiveOrg == "" {
			c.ActiveOrg = c.Organization
		}
		// Clear legacy fields so they don't get written back
		c.GitHubToken = ""
		c.Organization = ""
	}
}

func (c *Config) Validate() error {
	if len(c.Orgs) == 0 {
		return fmt.Errorf("no organizations configured.\n\n  Run: velocix config init")
	}
	active := c.GetActiveOrg()
	if active == nil {
		return fmt.Errorf("active organization %q not found in config", c.ActiveOrg)
	}
	if active.GitHubToken == "" {
		return fmt.Errorf("github token not configured for %q.\n\n  Set GITHUB_TOKEN env var or run: velocix config init", active.Name)
	}
	return nil
}

func (c *Config) GetActiveOrg() *OrgConfig {
	for i := range c.Orgs {
		if c.Orgs[i].Name == c.ActiveOrg || c.Orgs[i].Organization == c.ActiveOrg {
			return &c.Orgs[i]
		}
	}
	if len(c.Orgs) > 0 {
		return &c.Orgs[0]
	}
	return nil
}

func (c *Config) SetActiveOrg(name string) bool {
	for _, o := range c.Orgs {
		if o.Name == name || o.Organization == name {
			c.ActiveOrg = o.Name
			return true
		}
	}
	return false
}

func (c *Config) AddOrg(org OrgConfig) error {
	for _, o := range c.Orgs {
		if o.Name == org.Name {
			return fmt.Errorf("organization %q already exists. Use a different name or remove it first", org.Name)
		}
	}
	c.Orgs = append(c.Orgs, org)
	if len(c.Orgs) == 1 {
		c.ActiveOrg = org.Name
	}
	return nil
}

func (c *Config) RemoveOrg(name string) error {
	for i, o := range c.Orgs {
		if o.Name == name || o.Organization == name {
			c.Orgs = append(c.Orgs[:i], c.Orgs[i+1:]...)
			if c.ActiveOrg == name && len(c.Orgs) > 0 {
				c.ActiveOrg = c.Orgs[0].Name
			}
			return nil
		}
	}
	return fmt.Errorf("organization %q not found", name)
}

func (c *Config) Save() error {
	dir := filepath.Dir(DefaultConfigPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(DefaultConfigPath(), data, 0o644)
}

func SecondsToDuration(secs int) time.Duration {
	return time.Duration(secs) * time.Second
}
