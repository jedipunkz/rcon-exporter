package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"
)

func main() {
	listen := flag.String("web.listen-address", "127.0.0.1:9940", "Address to expose metrics on.")
	webConfig := flag.String("web.config.file", "", "Path to exporter-toolkit web config (TLS / basic auth).")
	path := flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	rconAddr := flag.String("rcon.address", "127.0.0.1:25575", "Minecraft RCON address.")
	timeout := flag.Duration("rcon.timeout", 5*time.Second, "Timeout for one scrape over RCON.")
	var level slog.LevelVar
	flag.TextVar(&level, "log.level", &level, "Log level (debug, info, warn, error).")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &level}))

	// Read from the environment so the password does not show up in process listings.
	password := os.Getenv("MINECRAFT_RCON_PASSWORD")
	if password == "" {
		logger.Error("MINECRAFT_RCON_PASSWORD is not set")
		os.Exit(1)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(&collector{addr: *rconAddr, password: password, timeout: *timeout, logger: logger})

	mux := http.NewServeMux()
	mux.Handle(*path, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		// Scrapes share the single-threaded RCON listener; don't pile them up.
		MaxRequestsInFlight: 1,
		Timeout:             *timeout + time.Second,
	}))

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	addrs := []string{*listen}
	systemd := false
	logger.Info("starting", "listen", *listen, "rcon", *rconAddr)
	if err := web.ListenAndServe(srv, &web.FlagConfig{WebListenAddresses: &addrs, WebSystemdSocket: &systemd, WebConfigFile: webConfig}, logger); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
