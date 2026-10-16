package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/egress/antigravity"
	"github.com/jzPPP3333/allproxyapi/internal/egress/qoder"
	"github.com/jzPPP3333/allproxyapi/internal/egress/workbuddy"
	"github.com/jzPPP3333/allproxyapi/internal/ingress"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// Build metadata, overridden at link time by build.sh.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "Print version information and exit")
	configPathFlag := flag.String("config", "./data/config.yaml", "Path to config file")
	portFlag := flag.Int("port", 0, "Server port (overrides config)")
	hostFlag := flag.String("host", "", "Server host (overrides config)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("AllProxyAPI %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	}

	// 1. Load configuration
	configPath := *configPathFlag
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("[Main] Failed to load config from %s: %v", configPath, err)
	}

	if *portFlag > 0 {
		cfg.Server.Port = *portFlag
	}
	if *hostFlag != "" {
		cfg.Server.Host = *hostFlag
	}

	// 2. Initialize Account Pool
	accountPool := pool.NewAccountPool()

	// 3. Initialize Egress Clients and register them as upstream adapters
	wbClient := workbuddy.NewClient()
	agClient := antigravity.NewClient(&cfg.Providers.Antigravity)
	qoderClient := qoder.NewClient(cfg.Providers.Qoder)

	upstreams := ingress.NewRegistry()
	upstreams.Register(ingress.NewWorkBuddyAdapter(wbClient, cfg.Providers.WorkBuddy.CheckinPath))
	upstreams.Register(ingress.NewAntigravityAdapter(agClient))
	upstreams.Register(ingress.NewQoderAdapter(qoderClient, cfg.Providers.Qoder.CheckinPath))

	// 4. Initialize Refresher
	refresher := pool.NewRefresher(accountPool, 60*time.Second)
	proxyURL := cfg.Providers.Antigravity.Proxy
	if proxyURL == "" {
		proxyURL = cfg.Server.Proxy
	}
	refresher.RegisterHandler(pool.TypeAntigravity, func(ctx context.Context, acc *pool.Account) error {
		return antigravity.RefreshToken(ctx, acc, cfg.Providers.Antigravity.ClientID, cfg.Providers.Antigravity.ClientSecret, proxyURL)
	})
	refresher.RegisterHandler(pool.TypeWorkBuddy, func(ctx context.Context, acc *pool.Account) error {
		return workbuddy.RefreshToken(ctx, acc)
	})
	refresher.RegisterHandler(pool.TypeQoder, func(ctx context.Context, acc *pool.Account) error {
		return qoder.RefreshAccount(ctx, acc, cfg.Providers.Qoder.Region, cfg.Providers.Qoder.AuthURL, cfg.Providers.Qoder.Proxy)
	})

	// 5. Scan and load accounts
	var dirsToScan []string
	seenDirs := make(map[string]bool)
	for _, dir := range cfg.Storage.OAuthDirs {
		if abs, err := filepath.Abs(dir); err == nil {
			if !seenDirs[abs] {
				seenDirs[abs] = true
				dirsToScan = append(dirsToScan, abs)
			}
		}
	}
	loaded := pool.ScanDirectories(accountPool, dirsToScan)
	log.Printf("[Main] Discovered and loaded %d accounts from storage locations", loaded)

	// Start auto-refresher
	refresher.Start()
	defer refresher.Stop()

	// Asynchronously discover models for all accounts on startup
	go func() {
		time.Sleep(1 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, acc := range accountPool.List() {
			up, ok := upstreams.Get(acc.Type)
			if !ok {
				continue
			}
			ms, err := up.FetchModels(ctx, acc)
			if err != nil || len(ms) == 0 {
				continue
			}
			accountPool.SetAccountModels(acc.ID, ms)
			log.Printf("[Main] %s 账号 %s 自动发现 %d 个可用模型", up.Label(), acc.Name, len(ms))
		}
	}()

	// 6. Initialize Ingress HTTP Server
	server := ingress.NewServer(cfg, accountPool, upstreams)

	// Daily check-in runs only when a provider has a check-in route configured.
	// The hour is taken from whichever provider enabled it.
	checkinHour := 0
	checkinEnabled := false
	if cfg.Providers.WorkBuddy.DailyCheckin && cfg.Providers.WorkBuddy.CheckinPath != "" {
		checkinEnabled = true
		checkinHour = cfg.Providers.WorkBuddy.CheckinHour
	}
	if cfg.Providers.Qoder.DailyCheckin && cfg.Providers.Qoder.CheckinPath != "" {
		checkinEnabled = true
		checkinHour = cfg.Providers.Qoder.CheckinHour
	}
	if checkinEnabled {
		sched := ingress.NewCheckinScheduler(server, checkinHour)
		server.AttachCheckinScheduler(sched)
		sched.Start()
		defer sched.Stop()
		log.Printf("[Main] 每日签到已启用，运行时间 %02d:00", checkinHour)
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: server.Handler(),
	}

	// Banner
	fmt.Println("==================================================================")
	fmt.Println("           AllProxyAPI Gateway - Multi Upstream Reverse Proxy      ")
	fmt.Println("     Google Antigravity · Tencent WorkBuddy · Alibaba Qoder        ")
	fmt.Println("==================================================================")
	fmt.Printf(" Upstreams       : %s\n", strings.Join(upstreams.IDs(), ", "))
	fmt.Printf(" Server Endpoint : http://%s\n", addr)
	fmt.Printf(" Web Dashboard   : http://%s/\n", addr)
	fmt.Printf(" OpenAI Endpoint : http://%s/v1/chat/completions\n", addr)
	fmt.Printf(" Anthropic API   : http://%s/v1/messages\n", addr)
	fmt.Printf(" API Key         : %s\n", cfg.Server.APIKey)
	fmt.Printf(" Loaded Accounts : %d\n", loaded)
	fmt.Println("==================================================================")

	// Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[Main] Server listen error: %v", err)
		}
	}()

	<-stopChan
	log.Println("[Main] Shutting down server gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("[Main] Server forced shutdown error: %v", err)
	}
	log.Println("[Main] Server stopped cleanly.")
}
