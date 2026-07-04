// Command proxy is the SimpleAPI lightweight AI protocol proxy entrypoint.
package main

import (
	"flag"
	"net/http"
	"time"

	"github.com/GreenTeodoro839/SimpleAPI/internal/config"
	"github.com/GreenTeodoro839/SimpleAPI/internal/httpapi"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/logging"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	listenAddr := flag.String("listen", "", "override server.listen")
	logLevel := flag.String("log-level", "info", "log level (debug|info|warn|error)")
	logJSON := flag.Bool("log-json", false, "emit JSON-formatted logs")
	flag.Parse()

	logger := logging.NewLogger(*logLevel, *logJSON)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}
	expanded := config.DeepCopy(cfg)
	config.Expand(expanded)
	if errs := config.Validate(expanded); len(errs) > 0 {
		for _, e := range errs {
			logger.Errorf("config invalid: [%s] %s: %s", e.Code, e.Path, e.Message)
		}
		logger.Fatalf("configuration validation failed (%d error(s)); exiting", len(errs))
	}

	idx, err := indexes.Build(expanded)
	if err != nil {
		logger.Fatalf("build indexes: %v", err)
	}
	rt := runtime.New(cfg, expanded, idx, *configPath)

	addr := *listenAddr
	if addr == "" {
		if cfg.Server.Listen != nil && *cfg.Server.Listen != "" {
			addr = *cfg.Server.Listen
		} else {
			addr = "127.0.0.1:8317"
		}
	}

	engine := httpapi.NewServer(rt, logger)
	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Infof("SimpleAPI listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("server: %v", err)
	}
}
