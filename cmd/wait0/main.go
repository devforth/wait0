package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"wait0/internal/wait0"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", getenvDefault("WAIT0_CONFIG", "/wait0.yaml"), "path to wait0.yaml")
	flag.Parse()

	cfg, err := wait0.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	svc, err := wait0.NewService(cfg)
	if err != nil {
		log.Fatalf("init service: %v", err)
	}
	defer svc.Close()

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}

	h := svc.Handler()

	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("wait0 listening on %s, origin=%s", addr, cfg.Server.Origin)
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func getenvDefault(name, def string) string {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	return v
}
