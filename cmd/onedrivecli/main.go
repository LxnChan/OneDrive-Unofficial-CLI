package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"onedrivecli/internal/auth"
	"onedrivecli/internal/config"
	"onedrivecli/internal/graph"
)

var globalUserAgent string
var globalVerbose bool

const defaultClientID = "540eb026-ab1b-4f9a-876d-86d9213ca6ce"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printUsage()
			return nil
		}
	}
	if err := validateFlagStyle(args); err != nil {
		return err
	}

	gfs := flag.NewFlagSet("global", flag.ContinueOnError)
	gfs.SetOutput(ioDiscard{})
	ua := gfs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := gfs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := gfs.Bool("verbose", false, "Enable verbose output")
	_ = gfs.Parse(args)
	rest := gfs.Args()
	if *ua != "" {
		globalUserAgent = *ua
		cfg, err := config.Load()
		if err == nil {
			cfg.UserAgent = globalUserAgent
			_ = config.Save(cfg)
		}
	}
	if *px != "" {
		cfg, err := config.Load()
		if err == nil {
			cfg.Proxy = *px
			_ = config.Save(cfg)
		}
	}
	if *verb {
		globalVerbose = true
	}
	args = rest
	if len(args) == 0 {
		printUsage()
		return nil
	}

	cmd := args[0]
	switch cmd {
	case "help", "-h", "--help":
		printUsage()
		return nil
	case "login":
		return cmdLogin(args[1:])
	case "logout":
		return cmdLogout(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "list":
		return cmdList(args[1:])
	case "upload":
		return cmdUpload(args[1:])
	case "download":
		return cmdDownload(args[1:])
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

func printUsage() {
	fmt.Println(strings.TrimSpace(`
onedrivecli - OneDrive CLI (Microsoft Graph + OAuth2 Device Code)

Usage:
  onedrivecli [--user-agent=<UA>] [--proxy=<MODE>] [--verbose=<true|false>] <command> [options]

Commands:
  login      Sign in (Device Code flow)
  logout     Sign out (clear local token)
  status     Show account and drive status
  list       List remote directory
  upload     Upload a file or folder
  download   Download a file or folder

Examples:
  onedrivecli --user-agent="MyApp/1.0" --proxy=system --verbose=true login --client-id=<APP_CLIENT_ID>
  onedrivecli status
  onedrivecli list --path=/Documents
  onedrivecli upload --local=./a.txt --remote=/Documents/a.txt
  onedrivecli download --remote=/Documents/a.txt --local=./a.txt
`))
}

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	clientID := fs.String("client-id", "", "Azure app (public client) client ID")
	tenant := fs.String("tenant", "", "Tenant: common (default) / consumers / organizations / or a specific tenant ID")
	scopes := fs.String("scopes", "", "Scopes (space or comma separated). Default: offline_access User.Read Files.ReadWrite.All")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	clientIDProvided := *clientID != ""
	tenantProvided := *tenant != ""
	if *ua != "" {
		globalUserAgent = *ua
	}
	if *verb {
		globalVerbose = true
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	isFirstConfig := !cfg.LoadedFromDisk

	usedDefaultClientID := false
	if !clientIDProvided {
		if cfg.ClientID != "" {
			*clientID = cfg.ClientID
		} else {
			*clientID = defaultClientID
			usedDefaultClientID = true
		}
	}

	if !tenantProvided {
		*tenant = cfg.Tenant
	}
	if *tenant == "" {
		*tenant = config.DefaultTenant()
	}

	if *scopes != "" {
		cfg.Scopes = splitScopes(*scopes)
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = config.DefaultScopes()
	}
	if globalUserAgent != "" {
		cfg.UserAgent = globalUserAgent
	}
	if *px != "" {
		cfg.Proxy = *px
	}
	if isFirstConfig {
		if usedDefaultClientID {
			fmt.Printf("Note: --client-id was not provided. Using built-in application ID: %s (override with --client-id=...)\n", defaultClientID)
		}
		if !tenantProvided && *tenant == "common" {
			fmt.Println("Note: --tenant was not provided. Using tenant=common (supports both personal and work accounts).")
		}
	}

	httpClient, err := httpClientFromConfig(cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	ac := auth.Client{
		ClientID:  *clientID,
		Tenant:    *tenant,
		Scopes:    cfg.Scopes,
		HTTP:      httpClient,
		UserAgent: cfg.UserAgent,
		Verbose:   globalVerbose,
	}

	dc, err := ac.DeviceCode(ctx)
	if err != nil {
		return err
	}
	fmt.Println(dc.Message)

	tr, err := ac.PollToken(ctx, dc)
	if err != nil {
		return err
	}

	cfg.ClientID = *clientID
	cfg.Tenant = *tenant
	cfg.Token = config.Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		Scope:        tr.Scope,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}
	if err := config.Save(cfg); err != nil {
		return err
	}

	fmt.Println("Signed in")
	return nil
}

func cmdLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ua != "" {
		globalUserAgent = *ua
	}
	if *verb {
		globalVerbose = true
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if globalUserAgent != "" {
		cfg.UserAgent = globalUserAgent
	}
	if *px != "" {
		cfg.Proxy = *px
	}
	config.ClearToken(cfg)
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Println("Signed out")
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ua != "" {
		globalUserAgent = *ua
	}
	if *verb {
		globalVerbose = true
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if globalUserAgent != "" {
		cfg.UserAgent = globalUserAgent
		_ = config.Save(cfg)
	}
	if *px != "" {
		cfg.Proxy = *px
		_ = config.Save(cfg)
	}

	ctx := context.Background()
	gc, err := graphClientFromConfig(cfg)
	if err != nil {
		return err
	}

	me, err := gc.Me(ctx)
	if err != nil {
		return err
	}
	drv, err := gc.Drive(ctx)
	if err != nil {
		return err
	}
	root, rootErr := gc.Root(ctx)
	recent, recentErr := gc.Recent(ctx, 5)

	email := me.Mail
	if email == "" {
		email = me.UserPrincipalName
	}

	fmt.Printf("User: %s (%s)\n", me.DisplayName, email)
	fmt.Printf("Tenant: %s\n", cfg.Tenant)
	fmt.Printf("Client ID: %s\n", cfg.ClientID)
	if cfg.UserAgent == "" {
		fmt.Printf("User-Agent: %s\n", "(default)")
	} else {
		fmt.Printf("User-Agent: %s\n", cfg.UserAgent)
	}
	if cfg.Proxy == "" {
		fmt.Printf("Proxy: %s\n", "system")
	} else {
		fmt.Printf("Proxy: %s\n", cfg.Proxy)
	}
	if !cfg.Token.ExpiresAt.IsZero() {
		d := time.Until(cfg.Token.ExpiresAt)
		if d < 0 {
			fmt.Printf("Access token expires at: %s (expired %s ago)\n", cfg.Token.ExpiresAt.Local().Format(time.RFC3339), (-d).Truncate(time.Second))
		} else {
			fmt.Printf("Access token expires at: %s (in %s)\n", cfg.Token.ExpiresAt.Local().Format(time.RFC3339), d.Truncate(time.Second))
		}
	} else {
		fmt.Printf("Access token expires at: %s\n", "(unknown)")
	}
	fmt.Printf("Refresh token present: %t\n", cfg.Token.RefreshToken != "")
	fmt.Printf("Drive: %s (%s)\n", drv.ID, drv.DriveType)
	fmt.Printf("Quota: total %s, used %s, remaining %s, deleted %s, state %s\n",
		graph.FormatBytes(drv.Quota.Total),
		graph.FormatBytes(drv.Quota.Used),
		graph.FormatBytes(drv.Quota.Remaining),
		graph.FormatBytes(drv.Quota.Deleted),
		drv.Quota.State,
	)
	if rootErr != nil || root == nil {
		fmt.Printf("Root: %s\n", "(unavailable)")
	} else {
		rootChildren := 0
		if root.Folder != nil {
			rootChildren = root.Folder.ChildCount
		}
		fmt.Printf("Root: %s (children %d)\n", root.Name, rootChildren)
	}
	if recentErr != nil {
		fmt.Printf("Recent: %s\n", "(unavailable)")
	} else {
		fmt.Printf("Recent: %d items\n", len(recent))
	}
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	remotePath := fs.String("path", "", "Remote directory path (e.g. /Documents). Empty means root.")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ua != "" {
		globalUserAgent = *ua
	}
	if *verb {
		globalVerbose = true
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if globalUserAgent != "" {
		cfg.UserAgent = globalUserAgent
		_ = config.Save(cfg)
	}
	if *px != "" {
		cfg.Proxy = *px
		_ = config.Save(cfg)
	}
	ctx := context.Background()
	gc, err := graphClientFromConfig(cfg)
	if err != nil {
		return err
	}

	items, err := gc.ListChildren(ctx, *remotePath)
	if err != nil {
		return err
	}
	for _, it := range items {
		kind := "F"
		size := graph.FormatBytes(it.Size)
		if it.Folder != nil {
			kind = "D"
			size = "-"
		}
		fmt.Printf("%s\t%s\t%s\n", kind, size, it.Name)
	}
	return nil
}

func cmdUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	localPath := fs.String("local", "", "Local file or folder path")
	remotePath := fs.String("remote", "", "Remote target path (file: includes filename; folder: target directory)")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *localPath == "" || *remotePath == "" {
		return errors.New("--local and --remote are required")
	}
	if *ua != "" {
		globalUserAgent = *ua
	}
	if *verb {
		globalVerbose = true
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if globalUserAgent != "" {
		cfg.UserAgent = globalUserAgent
		_ = config.Save(cfg)
	}
	if *px != "" {
		cfg.Proxy = *px
		_ = config.Save(cfg)
	}
	ctx := context.Background()
	gc, err := graphClientFromConfig(cfg)
	if err != nil {
		return err
	}

	st, err := os.Stat(*localPath)
	if err != nil {
		return err
	}

	if st.IsDir() {
		rp := *remotePath
		if strings.HasSuffix(rp, "/") || strings.HasSuffix(rp, "\\") {
			rp = strings.TrimRight(rp, "/\\")
		}
		if err := gc.UploadFolder(ctx, *localPath, rp); err != nil {
			return err
		}
		fmt.Println("Upload completed")
		return nil
	}

	rp := *remotePath
	if strings.HasSuffix(rp, "/") || strings.HasSuffix(rp, "\\") {
		rp = strings.TrimRight(rp, "/\\")
		rp = rp + "/" + filepath.Base(*localPath)
	}
	if err := gc.UploadFile(ctx, *localPath, rp); err != nil {
		return err
	}
	fmt.Println("Upload completed")
	return nil
}

func cmdDownload(args []string) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	remotePath := fs.String("remote", "", "Remote file or folder path")
	localPath := fs.String("local", "", "Local target path (file: target file; folder: target directory, optional)")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *remotePath == "" {
		return errors.New("--remote is required")
	}
	if *ua != "" {
		globalUserAgent = *ua
	}
	if *verb {
		globalVerbose = true
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if globalUserAgent != "" {
		cfg.UserAgent = globalUserAgent
		_ = config.Save(cfg)
	}
	if *px != "" {
		cfg.Proxy = *px
		_ = config.Save(cfg)
	}
	ctx := context.Background()
	gc, err := graphClientFromConfig(cfg)
	if err != nil {
		return err
	}

	if err := gc.DownloadItem(ctx, *remotePath, *localPath); err != nil {
		return err
	}
	fmt.Println("Download completed")
	return nil
}

func graphClientFromConfig(cfg *config.Config) (*graph.Client, error) {
	if cfg.ClientID == "" {
		return nil, errors.New("missing client_id; run login first")
	}
	if cfg.Token.RefreshToken == "" && cfg.Token.AccessToken == "" {
		return nil, errors.New("missing token; run login first")
	}

	httpClient, err := httpClientFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	ac := auth.Client{
		ClientID:  cfg.ClientID,
		Tenant:    cfg.Tenant,
		Scopes:    cfg.Scopes,
		HTTP:      httpClient,
		UserAgent: cfg.UserAgent,
		Verbose:   globalVerbose,
	}

	return &graph.Client{
		HTTP:      httpClient,
		UserAgent: cfg.UserAgent,
		Verbose:   globalVerbose,
		AccessToken: func(ctx context.Context) (string, error) {
			if cfg.Token.AccessToken != "" && time.Until(cfg.Token.ExpiresAt) > 2*time.Minute {
				return cfg.Token.AccessToken, nil
			}
			if cfg.Token.RefreshToken == "" {
				return "", errors.New("token expired; run login again")
			}
			tr, err := ac.Refresh(ctx, cfg.Token.RefreshToken)
			if err != nil {
				return "", err
			}
			cfg.Token.AccessToken = tr.AccessToken
			if tr.RefreshToken != "" {
				cfg.Token.RefreshToken = tr.RefreshToken
			}
			cfg.Token.TokenType = tr.TokenType
			cfg.Token.Scope = tr.Scope
			cfg.Token.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
			if err := config.Save(cfg); err != nil {
				return "", err
			}
			return cfg.Token.AccessToken, nil
		},
	}, nil
}

func httpClientFromConfig(cfg *config.Config) (*http.Client, error) {
	mode := strings.TrimSpace(cfg.Proxy)
	if mode == "" {
		mode = "system"
	}
	mode = strings.ToLower(mode)

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}, nil
	}
	tr := base.Clone()

	switch mode {
	case "system":
		tr.Proxy = http.ProxyFromEnvironment
	case "none":
		tr.Proxy = nil
	default:
		raw := mode
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("proxy scheme must be http or https")
		}
		if u.Host == "" {
			return nil, fmt.Errorf("proxy host is required")
		}
		tr.Proxy = http.ProxyURL(u)
	}

	return &http.Client{Transport: tr}, nil
}

func splitScopes(s string) []string {
	s = strings.ReplaceAll(s, ",", " ")
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, f := range fields {
		if f == "" {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (n int, err error) { return len(p), nil }

func validateFlagStyle(args []string) error {
	for _, a := range args {
		if a == "" {
			continue
		}
		if a == "-h" || a == "--help" {
			continue
		}
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "--") {
			if !strings.Contains(a, "=") {
				return fmt.Errorf("invalid flag format: %s (use --name=value, e.g. --verbose=true, --tenant=common)", a)
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("invalid flag format: %s (only --name=value is supported, e.g. --proxy=system)", a)
		}
	}
	return nil
}
