package main

import (
	"flag"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"

	"github.com/enrichman/httpgrace"
	"go.beppler.dev.br/wgproxy"
	"go.beppler.dev.br/wgproxy/middleware"
)

func main() {
	// configure options
	var address, configurationFile, pacFile string

	flag.StringVar(&address, "address", "localhost:1357", "address to listen on")
	flag.StringVar(&configurationFile, "configuration", "wg0.conf", "path to wireguard configuration file")
	flag.StringVar(&pacFile, "proxy-pac", "proxy.pac", "path to proxy.pac file")

	flag.Parse()

	logger := slog.New(middleware.NewRequestIdHandler(slog.Default().Handler()))

	logger.Info("starting proxy", "version", version(), "address", address, "configuration", configurationFile, "proxy-pac", pacFile)

	// configure proxy handler, logging and request id middlewares
	proxy, err := wgproxy.NewProxyFromFile(logger, configurationFile, pacFile)
	if err != nil {
		logger.Error("error configuring proxy", "error", err)
		os.Exit(1)
	}
	logging := middleware.NewLoggingMiddleware(proxy, logger)
	handler := middleware.NewRequestIdMiddleware(logging, false)

	// serve proxy requests
	if err := httpgrace.ListenAndServe(address, handler, httpgrace.WithLogger(logger)); err != nil {
		logger.Error("error running proxy", "error", err)
		os.Exit(1)
	}

	logger.Info("proxy stopped")
}

// version returns the main module version embedded by the Go toolchain.
func version() string {
	var version string
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "(devel)" {
		return "0.0.0-devel"
	}
	version = strings.TrimSuffix(info.Main.Version, "+dirty")
	modified := false
	revision := ""
	for _, info := range info.Settings {
		switch info.Key {
		case "vcs.revision":
			revision = info.Value
			if len(revision) > 7 {
				revision = revision[:7]
			}
		case "vcs.modified":
			modified = info.Value == "true"
		}
	}
	if modified {
		version += "-devel"
		if revision != "" {
			version += "+" + revision
		}
	}
	return version
}
