package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/skalluru/velocix/internal/config"
	"github.com/spf13/cobra"
)

func tokenURL(baseURL string) string {
	host := "https://github.com"
	if baseURL != "" {
		host = strings.TrimRight(baseURL, "/")
	}
	return host + "/settings/tokens/new?scopes=repo,read:org,actions&description=Velocix"
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage Velocix configuration",
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize configuration with guided setup",
	RunE:  runConfigInit,
}

var configAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new GitHub organization",
	RunE:  runConfigAdd,
}

var configRemoveCmd = &cobra.Command{
	Use:   "remove [name]",
	Short: "Remove a configured organization (use --all to remove everything)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConfigRemove,
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured organizations",
	RunE:  runConfigList,
}

var configUseCmd = &cobra.Command{
	Use:   "use [name]",
	Short: "Set the active organization",
	Args:  cobra.ExactArgs(1),
	RunE:  runConfigUse,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	RunE:  runConfigShow,
}

func init() {
	configCmd.AddCommand(configInitCmd)
	configCmd.AddCommand(configAddCmd)
	configRemoveCmd.Flags().BoolP("all", "a", false, "Remove all organizations and reset config")
	configCmd.AddCommand(configRemoveCmd)
	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configUseCmd)
	configCmd.AddCommand(configShowCmd)
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Printf("Config file:   %s\n", config.DefaultConfigPath())
	fmt.Printf("Active org:    %s\n", cfg.ActiveOrg)
	fmt.Printf("Poll interval: %s\n", cfg.PollInterval)
	fmt.Printf("Web port:      %d\n", cfg.WebPort)
	fmt.Printf("Data dir:      %s\n", cfg.DataDir)
	fmt.Println()
	if len(cfg.Orgs) == 0 {
		fmt.Println("No organizations configured. Run: velocix config add")
	} else {
		fmt.Printf("Organizations (%d):\n", len(cfg.Orgs))
		for _, o := range cfg.Orgs {
			marker := "  "
			if o.Name == cfg.ActiveOrg {
				marker = "* "
			}
			tokenDisplay := "(not set)"
			if o.GitHubToken != "" {
				tokenDisplay = o.GitHubToken[:4] + "..." + o.GitHubToken[len(o.GitHubToken)-4:]
			}
			urlDisplay := "github.com"
			if o.BaseURL != "" {
				urlDisplay = o.BaseURL
			}
			fmt.Printf("  %s%-20s  org: %-24s  url: %-30s  token: %s\n", marker, o.Name, o.Organization, urlDisplay, tokenDisplay)
		}
	}
	return nil
}

func runConfigList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Orgs) == 0 {
		fmt.Println("No organizations configured. Run: velocix config add")
		return nil
	}
	for _, o := range cfg.Orgs {
		marker := "  "
		if o.Name == cfg.ActiveOrg {
			marker = "* "
		}
		fmt.Printf("%s%s (%s)\n", marker, o.Name, o.Organization)
	}
	return nil
}

func runConfigAdd(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("  ◆ Add Organization")
	fmt.Println("  ─────────────────────────────────────")
	fmt.Println()

	fmt.Println("  For GitHub Enterprise, enter your server URL (e.g. https://github.example.com)")
	fmt.Print("  GitHub URL [https://github.com]: ")
	baseURL := readLine()
	if baseURL == "https://github.com" {
		baseURL = ""
	}

	fmt.Println()
	fmt.Println("  Velocix needs a token with scopes: repo, read:org, actions")
	tURL := tokenURL(baseURL)
	opened := tryOpenBrowser(tURL)
	if opened {
		fmt.Println("  Browser opened to create a new token.")
	} else {
		fmt.Println("  Open this URL to create a token:")
		fmt.Printf("  %s\n", tURL)
	}
	fmt.Println()
	fmt.Print("  GitHub Token: ")
	token := readLine()
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
		if token != "" {
			fmt.Println("  Using GITHUB_TOKEN from environment.")
		}
	}

	fmt.Println()
	fmt.Print("  GitHub organization: ")
	org := readLine()
	if org == "" {
		return fmt.Errorf("organization is required")
	}

	fmt.Print("  Display name (e.g. work, personal): ")
	name := readLine()
	if name == "" {
		return fmt.Errorf("name is required")
	}

	orgCfg := config.OrgConfig{
		Name:         name,
		GitHubToken:  token,
		Organization: org,
		BaseURL:      baseURL,
	}

	if err := cfg.AddOrg(orgCfg); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("  Added %q (%s)\n", name, org)
	if len(cfg.Orgs) == 1 {
		fmt.Printf("  Set as active organization.\n")
	} else {
		fmt.Printf("  Switch to it: velocix config use %s\n", name)
	}
	fmt.Println()
	fmt.Println("  Restart velocix to pick up the new org.")
	return nil
}

func runConfigRemove(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")

	if all {
		cfg := config.DefaultConfig()
		cfg.Orgs = nil
		cfg.ActiveOrg = ""
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Println("All organizations removed. Config reset to defaults.")
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("provide an org name or use --all to remove everything")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := args[0]
	if err := cfg.RemoveOrg(name); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Removed %q\n", name)
	return nil
}

func runConfigUse(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := args[0]
	if !cfg.SetActiveOrg(name) {
		return fmt.Errorf("organization %q not found. Run: velocix config list", name)
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Active organization set to %q\n", name)
	return nil
}

func runConfigInit(cmd *cobra.Command, args []string) error {
	path := config.DefaultConfigPath()

	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Config already exists at %s\n", path)
		fmt.Print("Overwrite? [y/N]: ")
		answer := readLine()
		if !strings.HasPrefix(strings.ToLower(answer), "y") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	fmt.Println()
	fmt.Println("  ◆ Velocix Setup")
	fmt.Println("  ─────────────────────────────────────")
	fmt.Println()

	fmt.Println("  Step 1: GitHub URL")
	fmt.Println()
	fmt.Println("  For GitHub Enterprise, enter your server URL (e.g. https://github.example.com)")
	fmt.Print("  GitHub URL [https://github.com]: ")
	baseURL := readLine()
	if baseURL == "https://github.com" {
		baseURL = ""
	}

	fmt.Println()
	fmt.Println("  Step 2: GitHub Personal Access Token")
	fmt.Println()
	fmt.Println("  Velocix needs a token with these scopes: repo, read:org, actions")
	fmt.Println()

	tURL := tokenURL(baseURL)
	opened := tryOpenBrowser(tURL)
	if opened {
		fmt.Println("  Your browser has been opened to create a new token.")
	} else {
		fmt.Println("  Open this URL to create a new token:")
		fmt.Println()
		fmt.Printf("  %s\n", tURL)
	}

	fmt.Println()
	fmt.Println("  After creating the token, paste it below.")
	fmt.Println("  (You can also set GITHUB_TOKEN env var instead and press Enter to skip)")
	fmt.Println()
	fmt.Print("  GitHub Token: ")
	token := readLine()

	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
		if token != "" {
			fmt.Println("  Using GITHUB_TOKEN from environment.")
		}
	}

	fmt.Println()
	fmt.Println("  Step 3: GitHub Organization")
	fmt.Println()
	fmt.Print("  Organization name: ")
	org := readLine()
	if org == "" {
		return fmt.Errorf("organization cannot be empty")
	}

	fmt.Println()
	fmt.Println("  Step 4: Optional Settings (press Enter for defaults)")
	fmt.Println()
	fmt.Print("  Web port [8080]: ")
	portStr := readLine()
	port := 8080
	if portStr != "" {
		fmt.Sscanf(portStr, "%d", &port)
	}

	fmt.Print("  Poll interval in seconds [30]: ")
	intervalStr := readLine()
	intervalSec := 30
	if intervalStr != "" {
		fmt.Sscanf(intervalStr, "%d", &intervalSec)
	}

	cfg := config.DefaultConfig()
	cfg.WebPort = port
	cfg.PollInterval = config.SecondsToDuration(intervalSec)
	cfg.Orgs = []config.OrgConfig{
		{
			Name:         org,
			GitHubToken:  token,
			Organization: org,
			BaseURL:      baseURL,
		},
	}
	cfg.ActiveOrg = org

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Println()
	fmt.Println("  ─────────────────────────────────────")
	fmt.Printf("  Config saved to %s\n", path)
	fmt.Println()
	fmt.Println("  Get started:")
	fmt.Printf("    velocix serve     # Web dashboard at http://localhost:%d\n", port)
	fmt.Println("    velocix tui       # Terminal dashboard")
	fmt.Println()
	fmt.Println("  Add more orgs later:")
	fmt.Println("    velocix config add")
	fmt.Println()

	if token == "" {
		fmt.Println("  Note: No token was saved. Set GITHUB_TOKEN env var before running.")
		fmt.Println()
	}

	return nil
}

func readLine() string {
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

func tryOpenBrowser(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return false
	}
	return cmd.Start() == nil
}
