package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	plugin "imuslab.com/zoraxy/mod/plugins/zoraxy_plugin"

	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/config"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/reconciler"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/status"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/technitium"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/uiapi"
	"github.com/jasonlaguidice/zoraxy-technitium-sync/internal/zoraxyclient"
)

const (
	pluginID   = "com.github.jasonlaguidice.zoraxy-technitium-sync"
	uiPath     = "/ui"
	webRoot    = "/www"
	configFile = "config.json"
)

// LICENSE and NOTICE ride along with the panel: the release assets are bare
// binaries, so the text has nowhere else to travel. See license.go.
//
//go:embed www/* LICENSE NOTICE
var content embed.FS

func main() {
	versionMajor, versionMinor, versionPatch := parseVersion(version)
	runtimeCfg, err := plugin.ServeAndRecvSpec(&plugin.IntroSpect{
		ID:            pluginID,
		Name:          "Technitium Sync",
		Author:        "jasonlaguidice",
		AuthorContact: "https://matrix.to/#/@jason:shadowdrake.org",
		Description:   "Keeps Technitium DNS A/AAAA records in sync with Zoraxy's HTTP proxy host rules",
		URL:           "https://github.com/jasonlaguidice/zoraxy-technitium-sync",
		Type:          plugin.PluginType_Utilities,
		VersionMajor:  versionMajor,
		VersionMinor:  versionMinor,
		VersionPatch:  versionPatch,

		UIPath: uiPath,

		PermittedAPIEndpoints: []plugin.PermittedAPIEndpoint{
			{
				Method:   http.MethodGet,
				Endpoint: "/plugin/api/proxy/list",
				Reason:   "Read configured HTTP proxy host rules (hostname + aliases + enabled state) to compute which DNS records should exist",
			},
		},
	})
	if err != nil {
		fmt.Printf("failed to initialise plugin: %v\n", err)
		os.Exit(1)
	}

	configPath := configFile
	if exe, err := os.Executable(); err == nil {
		configPath = filepath.Join(filepath.Dir(exe), configFile)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Printf("failed to load config %q: %v\n", configPath, err)
		os.Exit(1)
	}
	configStore := config.NewStore(cfg, configPath)
	statusStore := &status.Store{}
	trigger := make(chan struct{}, 1)

	zoraxyClient := zoraxyclient.New(runtimeCfg.ZoraxyPort, runtimeCfg.APIKey)
	techClient := technitium.New(cfg.TechnitiumBaseURL, cfg.TechnitiumToken, cfg.Zone, cfg.TTLSeconds, cfg.InstanceID)
	rec := reconciler.New(zoraxyClient, techClient, reconcilerOptions(cfg))

	mux := http.NewServeMux()

	api := &uiapi.Server{Config: configStore, Status: statusStore, Trigger: trigger}
	api.Register(uiPath, mux)

	RegisterLicenseRoutes(uiPath, mux)

	uiRouter := plugin.NewPluginEmbedUIRouter(pluginID, &content, webRoot, uiPath)
	uiRouter.RegisterTerminateHandler(func() {
		fmt.Println("Technitium Sync terminating")
	}, mux)
	uiRouter.AttachHandlerToMux(mux)

	go runReconcileLoop(techClient, configStore, statusStore, rec, trigger)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		fmt.Println("Technitium Sync stopping")
		os.Exit(0)
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", runtimeCfg.Port)
	fmt.Printf("Technitium Sync listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Printf("http server exited: %v\n", err)
		os.Exit(1)
	}
}

func reconcilerOptions(c *config.Config) reconciler.Options {
	return reconciler.Options{
		IPv4Target:  c.LANIPv4,
		AAAAEnabled: c.AAAAEnabled,
		IPv6Target:  c.LANIPv6,
	}
}

// runReconcileLoop re-reads the config store on every cycle (so UI edits
// take effect on the next tick or immediately via trigger) but keeps a
// single Reconciler alive for the whole run: recreating it every cycle would
// reset its circuit-breaker baseline (lastGoodHostCount) each time.
func runReconcileLoop(techClient *technitium.Client, configStore *config.Store, statusStore *status.Store, rec *reconciler.Reconciler, trigger <-chan struct{}) {
	runOnce := func() {
		cfg := configStore.Snapshot()
		techClient.BaseURL = cfg.TechnitiumBaseURL
		techClient.Token = cfg.TechnitiumToken
		techClient.Zone = cfg.Zone
		techClient.TTLSeconds = cfg.TTLSeconds
		techClient.InstanceID = cfg.InstanceID
		rec.Opts = reconcilerOptions(&cfg)

		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		result, err := rec.Reconcile(ctx)
		cancel()

		snap := status.Snapshot{
			LastPollTime:     time.Now(),
			ManagedCount:     result.ManagedCount,
			Created:          result.Created,
			Updated:          result.Updated,
			Deleted:          result.Deleted,
			Skipped:          result.Skipped,
			DeletionsSkipped: result.DeletionsSkipped,
		}
		if err != nil {
			snap.LastSuccess = false
			snap.LastError = err.Error()
			log.Printf("reconcile failed: %v", err)
		} else {
			snap.LastSuccess = len(result.Errors) == 0
			if len(result.Errors) > 0 {
				snap.LastError = strings.Join(result.Errors, "; ")
				log.Printf("reconcile completed with errors: %s", snap.LastError)
			}
			for _, h := range result.Skipped {
				log.Printf("info: %s already has a DNS record without our ownership marker, leaving it untouched", h)
			}
			for _, h := range result.Created {
				log.Printf("created DNS record(s) for %s", h)
			}
			for _, h := range result.Updated {
				log.Printf("updated DNS record(s) for %s", h)
			}
			for _, h := range result.Deleted {
				log.Printf("deleted DNS record(s) for removed host %s", h)
			}
			if result.DeletionsSkipped {
				log.Printf("warning: skipped deletions this cycle - zoraxy's proxy host list looked too small (possible transient glitch)")
			}
		}
		statusStore.Set(snap)
	}

	runOnce()
	interval := time.Duration(configStore.Snapshot().PollIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			runOnce()
		case <-trigger:
			runOnce()
		}
		if next := time.Duration(configStore.Snapshot().PollIntervalSeconds) * time.Second; next != interval {
			interval = next
			ticker.Reset(interval)
		}
	}
}
