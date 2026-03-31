package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	cfgPath := gfs.String("config", "", "Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json")
	ua := gfs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := gfs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := gfs.Bool("verbose", false, "Enable verbose output")
	_ = gfs.Parse(args)
	rest := gfs.Args()
	if *cfgPath != "" {
		if err := config.SetPath(*cfgPath); err != nil {
			return err
		}
	}
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
onedrivecli - OneDrive CLI by LxnChan
https://lxnchan.cn

Usage:
  onedrivecli [--config=<PATH>] [--user-agent=<UA>] [--proxy=<MODE>] [--verbose=<true|false>] <command> [options]

Commands:
  login
  logout
  status
  list
  upload
  download

Parameters:
  Global:
    --config=<PATH>           Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json
    --user-agent=<UA>         Set User-Agent for all requests and persist to config.json
    --proxy=<MODE>            Proxy: system/none or http(s)://host:port or host:port (will persist to config.json)
    --verbose=<true|false>    Enable verbose output

  login:
    --client-id=<ID>          Azure app (public client) client ID (default: built-in)
    --tenant=<TENANT>         common/consumers/organizations or a specific tenant ID (default: common)
    --scopes=<SCOPES>         Space or comma separated scopes (default: offline_access User.Read Files.ReadWrite.All)

  list:
    --path=<REMOTE_PATH>      Remote directory path (default: root)

  upload:
    --local=<LOCAL_PATH>      Local file or folder path
    --remote=<REMOTE_PATH>    Remote target path
    --chunk-size=<SIZE>       Chunk size (5MiB-60MiB, default: 10MiB)
    --threads=<N>             Threads (default: 2)

  download:
    --remote=<REMOTE_PATH>    Remote file or folder path
    --local=<LOCAL_PATH>      Local target path (optional for folders)
    --chunk-size=<SIZE>       Chunk size (5MiB-60MiB, default: 10MiB)
    --threads=<N>             Threads (default: 2)

Examples:
  onedrivecli --config=./config.json status
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
	cfgPath := fs.String("config", "", "Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json")
	clientID := fs.String("client-id", "", "Azure app (public client) client ID")
	tenant := fs.String("tenant", "", "Tenant: common (default) / consumers / organizations / or a specific tenant ID")
	scopes := fs.String("scopes", "", "Scopes (space or comma separated). Default: offline_access User.Read Files.ReadWrite.All")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath != "" {
		if err := config.SetPath(*cfgPath); err != nil {
			return err
		}
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
			fmt.Printf("[Info] --client-id was not provided. Using built-in application ID. (you can override with --client-id=...)\n")
		}
		if !tenantProvided && *tenant == "common" {
			fmt.Println("[Info] --tenant was not provided. Using tenant=common.")
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
	cfgPath := fs.String("config", "", "Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath != "" {
		if err := config.SetPath(*cfgPath); err != nil {
			return err
		}
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
	cfgPath := fs.String("config", "", "Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath != "" {
		if err := config.SetPath(*cfgPath); err != nil {
			return err
		}
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
	if globalVerbose {
		fmt.Printf("Drive: %s (%s)\n", drv.ID, drv.DriveType)
	} else {
		fmt.Printf("Drive: %s (id: %s)\n", drv.DriveType, maskID(drv.ID))
	}
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
		fmt.Printf("Root: %s (%d children)\n", root.Name, rootChildren)
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
	cfgPath := fs.String("config", "", "Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath != "" {
		if err := config.SetPath(*cfgPath); err != nil {
			return err
		}
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
	cfgPath := fs.String("config", "", "Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	chunkSizeStr := fs.String("chunk-size", "", "Chunk size for transfers (e.g. 10MiB, 10485760)")
	threadsFlag := fs.Int("threads", 0, "Number of threads (upload: chunk workers for a file; for a folder: concurrent files)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath != "" {
		if err := config.SetPath(*cfgPath); err != nil {
			return err
		}
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

	chunkSize := cfg.UploadChunkSize
	if flagProvided(args, "chunk-size") {
		v, err := parseByteSize(*chunkSizeStr)
		if err != nil {
			return err
		}
		chunkSize = v
		cfg.UploadChunkSize = v
		_ = config.Save(cfg)
	}
	threads := cfg.UploadThreads
	if flagProvided(args, "threads") {
		if *threadsFlag <= 0 {
			return errors.New("--threads must be >= 1")
		}
		threads = *threadsFlag
		cfg.UploadThreads = threads
		_ = config.Save(cfg)
	}
	if threads <= 0 {
		threads = 2
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
		remoteBase := strings.TrimRight(*remotePath, "/\\")
		remoteBase = strings.ReplaceAll(remoteBase, "\\", "/")

		type job struct {
			local  string
			remote string
			size   int64
			chunks int64
		}

		var jobs []job
		var totalBytes int64
		var totalChunks int64
		normalizedChunk := normalizeUploadChunkSize(chunkSize)

		err := filepath.WalkDir(*localPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(*localPath, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			rp := remoteBase
			if rp == "" {
				rp = "/" + rel
			} else {
				if !strings.HasPrefix(rp, "/") {
					rp = "/" + rp
				}
				rp = rp + "/" + rel
			}
			chunks := int64(1)
			const maxSimpleUpload = 4 * 1024 * 1024
			if info.Size() > maxSimpleUpload {
				chunks = (info.Size() + normalizedChunk - 1) / normalizedChunk
			}
			jobs = append(jobs, job{local: path, remote: rp, size: info.Size(), chunks: chunks})
			totalBytes += info.Size()
			totalChunks += chunks
			return nil
		})
		if err != nil {
			return err
		}

		if remoteBase != "" {
			if !strings.HasPrefix(remoteBase, "/") {
				remoteBase = "/" + remoteBase
			}
			if err := gc.EnsureRemoteFolder(ctx, remoteBase); err != nil {
				return err
			}
		}

		var bytesDone int64
		var chunksDone int64
		pp := newProgressPrinter("upload", totalBytes, totalChunks, threads, &bytesDone, &chunksDone)
		pp.Start()
		defer pp.Stop()

		workCh := make(chan job, threads)
		errCh := make(chan error, 1)
		var wg sync.WaitGroup
		for i := 0; i < threads; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range workCh {
					parent := path.Dir(j.remote)
					parent = strings.TrimSuffix(parent, "/")
					if parent == "." {
						parent = ""
					}
					if parent != "" && parent != "/" {
						if err := gc.EnsureRemoteFolder(ctx, parent); err != nil {
							select {
							case errCh <- err:
							default:
							}
							return
						}
					}
					if err := gc.UploadFileWithOptions(ctx, j.local, j.remote, graph.TransferOptions{
						ChunkSize: normalizedChunk,
						Threads:   1,
						Callbacks: graph.TransferCallbacks{
							OnBytes: func(n int64) { atomic.AddInt64(&bytesDone, n) },
							OnChunk: func() { atomic.AddInt64(&chunksDone, 1) },
						},
					}); err != nil {
						select {
						case errCh <- err:
						default:
						}
						return
					}
				}
			}()
		}
		for _, j := range jobs {
			workCh <- j
		}
		close(workCh)
		wg.Wait()

		select {
		case err := <-errCh:
			return err
		default:
		}
		pp.Stop()
		fmt.Println("Upload completed")
		return nil
	}

	rp := *remotePath
	if strings.HasSuffix(rp, "/") || strings.HasSuffix(rp, "\\") {
		rp = strings.TrimRight(rp, "/\\")
		rp = rp + "/" + filepath.Base(*localPath)
	}
	rp = strings.ReplaceAll(rp, "\\", "/")
	if !strings.HasPrefix(rp, "/") {
		rp = "/" + rp
	}

	normalizedChunk := normalizeUploadChunkSize(chunkSize)
	const maxSimpleUpload = 4 * 1024 * 1024
	totalBytes := st.Size()
	totalChunks := int64(1)
	if st.Size() > maxSimpleUpload {
		totalChunks = (st.Size() + normalizedChunk - 1) / normalizedChunk
	}
	var bytesDone int64
	var chunksDone int64
	pp := newProgressPrinter("upload", totalBytes, totalChunks, threads, &bytesDone, &chunksDone)
	pp.Start()
	defer pp.Stop()

	if err := gc.UploadFileWithOptions(ctx, *localPath, rp, graph.TransferOptions{
		ChunkSize: normalizedChunk,
		Threads:   threads,
		Callbacks: graph.TransferCallbacks{
			OnBytes: func(n int64) { atomic.AddInt64(&bytesDone, n) },
			OnChunk: func() { atomic.AddInt64(&chunksDone, 1) },
		},
	}); err != nil {
		return err
	}
	pp.Stop()
	fmt.Println("Upload completed")
	return nil
}

func cmdDownload(args []string) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	remotePath := fs.String("remote", "", "Remote file or folder path")
	localPath := fs.String("local", "", "Local target path (file: target file; folder: target directory, optional)")
	cfgPath := fs.String("config", "", "Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json")
	ua := fs.String("user-agent", "", "Set User-Agent for all requests and persist to config.json")
	px := fs.String("proxy", "", "Proxy: system/none or http(s)://host:port or host:port (persist to config.json)")
	chunkSizeStr := fs.String("chunk-size", "", "Chunk size for transfers (e.g. 8MiB, 10485760)")
	threadsFlag := fs.Int("threads", 0, "Number of threads (download: range workers for a file; for a folder: concurrent files)")
	verb := fs.Bool("verbose", false, "Enable verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath != "" {
		if err := config.SetPath(*cfgPath); err != nil {
			return err
		}
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

	chunkSize := cfg.DownloadChunkSize
	if flagProvided(args, "chunk-size") {
		v, err := parseByteSize(*chunkSizeStr)
		if err != nil {
			return err
		}
		chunkSize = v
		cfg.DownloadChunkSize = v
		_ = config.Save(cfg)
	}
	threads := cfg.DownloadThreads
	if flagProvided(args, "threads") {
		if *threadsFlag <= 0 {
			return errors.New("--threads must be >= 1")
		}
		threads = *threadsFlag
		cfg.DownloadThreads = threads
		_ = config.Save(cfg)
	}
	if threads <= 0 {
		threads = 2
	}
	ctx := context.Background()
	gc, err := graphClientFromConfig(cfg)
	if err != nil {
		return err
	}

	it, err := gc.GetItemByPath(ctx, *remotePath)
	if err != nil {
		return err
	}
	normalizedChunk := normalizeDownloadChunkSize(chunkSize)

	if it.Folder == nil {
		totalBytes := it.Size
		totalChunks := int64(1)
		if it.Size > 0 {
			totalChunks = (it.Size + normalizedChunk - 1) / normalizedChunk
		}
		var bytesDone int64
		var chunksDone int64
		pp := newProgressPrinter("download", totalBytes, totalChunks, threads, &bytesDone, &chunksDone)
		pp.Start()
		defer pp.Stop()

		if err := gc.DownloadFileByPathWithOptions(ctx, *remotePath, *localPath, graph.TransferOptions{
			ChunkSize: normalizedChunk,
			Threads:   threads,
			Callbacks: graph.TransferCallbacks{
				OnBytes: func(n int64) { atomic.AddInt64(&bytesDone, n) },
				OnChunk: func() { atomic.AddInt64(&chunksDone, 1) },
			},
		}); err != nil {
			return err
		}
		pp.Stop()
		fmt.Println("Download completed")
		return nil
	}

	baseLocal := *localPath
	if baseLocal == "" {
		base := filepath.Base(filepath.ToSlash(strings.TrimRight(*remotePath, "/")))
		if base == "" || base == "." || base == "/" {
			base = "onedrive_folder"
		}
		baseLocal = base
	}
	if err := os.MkdirAll(baseLocal, 0o755); err != nil {
		return err
	}

	type job struct {
		remote string
		local  string
		size   int64
		chunks int64
	}

	var jobs []job
	var totalBytes int64
	var totalChunks int64

	var walk func(remoteDir, localDir string) error
	walk = func(remoteDir, localDir string) error {
		children, err := gc.ListChildren(ctx, remoteDir)
		if err != nil {
			return err
		}
		for _, child := range children {
			cr := strings.TrimRight(remoteDir, "/")
			var childRemote string
			if cr == "" {
				childRemote = "/" + child.Name
			} else {
				childRemote = cr + "/" + child.Name
			}
			childLocal := filepath.Join(localDir, child.Name)
			if child.Folder != nil {
				if err := os.MkdirAll(childLocal, 0o755); err != nil {
					return err
				}
				if err := walk(childRemote, childLocal); err != nil {
					return err
				}
				continue
			}
			chunks := int64(1)
			if child.Size > 0 {
				chunks = (child.Size + normalizedChunk - 1) / normalizedChunk
			}
			jobs = append(jobs, job{remote: childRemote, local: childLocal, size: child.Size, chunks: chunks})
			totalBytes += child.Size
			totalChunks += chunks
		}
		return nil
	}
	if err := walk(*remotePath, baseLocal); err != nil {
		return err
	}

	var bytesDone int64
	var chunksDone int64
	pp := newProgressPrinter("download", totalBytes, totalChunks, threads, &bytesDone, &chunksDone)
	pp.Start()
	defer pp.Stop()

	workCh := make(chan job, threads)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range workCh {
				if err := gc.DownloadFileByPathWithOptions(ctx, j.remote, j.local, graph.TransferOptions{
					ChunkSize: normalizedChunk,
					Threads:   1,
					Callbacks: graph.TransferCallbacks{
						OnBytes: func(n int64) { atomic.AddInt64(&bytesDone, n) },
						OnChunk: func() { atomic.AddInt64(&chunksDone, 1) },
					},
				}); err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			}
		}()
	}
	for _, j := range jobs {
		workCh <- j
	}
	close(workCh)
	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}
	pp.Stop()
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

func flagProvided(args []string, name string) bool {
	prefix := "--" + name + "="
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func maskID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(unknown)"
	}
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "..." + s[len(s)-6:]
}

func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("byte size is required")
	}

	upper := strings.ToUpper(s)
	mults := map[string]int64{
		"B":   1,
		"KB":  1000,
		"MB":  1000 * 1000,
		"GB":  1000 * 1000 * 1000,
		"KIB": 1024,
		"MIB": 1024 * 1024,
		"GIB": 1024 * 1024 * 1024,
	}

	for suf, m := range mults {
		if strings.HasSuffix(upper, suf) && len(upper) > len(suf) {
			num := strings.TrimSpace(upper[:len(upper)-len(suf)])
			v, err := strconv.ParseInt(num, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid byte size: %s", s)
			}
			if v < 0 {
				return 0, fmt.Errorf("invalid byte size: %s", s)
			}
			return v * m, nil
		}
	}

	v, err := strconv.ParseInt(upper, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size: %s", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("invalid byte size: %s", s)
	}
	return v, nil
}

func normalizeUploadChunkSize(n int64) int64 {
	const min = 5 * 1024 * 1024
	const max = 60 * 1024 * 1024
	if n <= 0 {
		return 10 * 1024 * 1024
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	n = (n / (1 * 1024 * 1024)) * (1 * 1024 * 1024)
	if n < min {
		n = min
	}
	return n
}

func normalizeDownloadChunkSize(n int64) int64 {
	const min = 5 * 1024 * 1024
	const max = 60 * 1024 * 1024
	if n <= 0 {
		return 10 * 1024 * 1024
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

type progressPrinter struct {
	label       string
	totalBytes  int64
	totalChunks int64
	threads     int
	bytesDone   *int64
	chunksDone  *int64

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func newProgressPrinter(label string, totalBytes, totalChunks int64, threads int, bytesDone, chunksDone *int64) *progressPrinter {
	if totalBytes < 0 {
		totalBytes = 0
	}
	if totalChunks < 1 {
		totalChunks = 1
	}
	if threads < 1 {
		threads = 1
	}
	return &progressPrinter{
		label:       label,
		totalBytes:  totalBytes,
		totalChunks: totalChunks,
		threads:     threads,
		bytesDone:   bytesDone,
		chunksDone:  chunksDone,
		stopCh:      make(chan struct{}),
	}
}

func (p *progressPrinter) Start() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		start := time.Now()
		var lastTime = start
		var lastBytes int64

		for {
			select {
			case <-p.stopCh:
				p.render(time.Since(start), 0, true)
				return
			case <-ticker.C:
				now := time.Now()
				done := atomic.LoadInt64(p.bytesDone)
				dt := now.Sub(lastTime)
				var speed float64
				if dt > 0 {
					speed = float64(done-lastBytes) / dt.Seconds()
				}
				lastTime = now
				lastBytes = done
				p.render(now.Sub(start), speed, false)
			}
		}
	}()
}

func (p *progressPrinter) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.wg.Wait()
}

func (p *progressPrinter) render(elapsed time.Duration, speed float64, final bool) {
	done := atomic.LoadInt64(p.bytesDone)
	chunks := atomic.LoadInt64(p.chunksDone)
	if chunks < 0 {
		chunks = 0
	}
	if chunks > p.totalChunks {
		chunks = p.totalChunks
	}
	if done < 0 {
		done = 0
	}
	if done > p.totalBytes && p.totalBytes > 0 {
		done = p.totalBytes
	}

	percent := float64(0)
	if p.totalBytes > 0 {
		percent = float64(done) * 100 / float64(p.totalBytes)
	}

	speedStr := "-"
	if speed > 0 {
		speedStr = graph.FormatBytes(int64(speed)) + "/s"
	}

	line := fmt.Sprintf(
		"%s %6.2f%% %s/%s  speed %s  threads %d  chunks %d/%d",
		p.label,
		percent,
		graph.FormatBytes(done),
		graph.FormatBytes(p.totalBytes),
		speedStr,
		p.threads,
		chunks,
		p.totalChunks,
	)

	if final {
		fmt.Fprintln(os.Stderr, "\r"+line)
		return
	}
	fmt.Fprint(os.Stderr, "\r"+line)
	_ = elapsed
}
