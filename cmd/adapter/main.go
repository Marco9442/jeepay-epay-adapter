package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/marco9442/jeepay-epay-adapter/internal/app"
	"github.com/marco9442/jeepay-epay-adapter/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := app.FromEnv()
	if err != nil {
		log.Error("配置无效", "err", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.SQLitePath)
	if err != nil {
		log.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	svc := app.New(cfg, st, &http.Client{Timeout: 20 * time.Second})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go (&app.Worker{Svc: svc, Log: log, Every: cfg.NotifyInterval, MaxTry: cfg.NotifyMaxTries}).Run(ctx)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           app.Handler(svc, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Info("jeepay-epay-adapter 已启动", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP 服务退出", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
