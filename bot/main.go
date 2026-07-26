// Команда monbot — Telegram-бот мониторинга для домашнего стека.
//
// Делает две вещи одновременно (в двух горутинах):
//  1. Держит HTTP-сервер, принимающий вебхуки от Alertmanager, и шлёт алерты в чат.
//  2. Ведёт long-polling Telegram и отвечает на команды (/status, /top, /logs, ...).
//
// Обе горутины живут под общим context. По SIGTERM/SIGINT (их шлёт `docker stop`)
// контекст отменяется — и сервер, и поллинг корректно завершаются (graceful shutdown).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	// Вшиваем базу таймзон прямо в бинарь. Финальный образ — scratch (пустой),
	// в нём нет /usr/share/zoneinfo, а нам нужно локальное время в сообщениях.
	// С этим импортом TZ=Europe/Moscow из ENV подхватится без внешних файлов.
	_ "time/tzdata"

	"monbot/internal/config"
	"monbot/internal/dockerx"
	"monbot/internal/prometheus"
	"monbot/internal/telegram"
	"monbot/internal/webhook"
)

func main() {
	// slog с текстовым хендлером — читаемо в `docker logs`. Уровень из ENV.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel(os.Getenv("LOG_LEVEL")),
	}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("фатальная ошибка", "err", err)
		os.Exit(1)
	}
	logger.Info("бот остановлен штатно")
}

// run вынесен отдельно от main, чтобы возвращать ошибку (а не звать os.Exit
// из глубины) — так проще тестировать и понятнее поток управления.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Docker-клиент: закрываем при выходе.
	docker, err := dockerx.New(cfg.DockerHost)
	if err != nil {
		return err
	}
	defer docker.Close()

	prom := prometheus.New(cfg.PrometheusURL)

	bot, err := telegram.New(cfg, prom, docker, logger)
	if err != nil {
		return err
	}

	// ctx отменяется при SIGINT/SIGTERM. stop() восстанавливает поведение
	// сигналов по умолчанию и одновременно служит нам «ручным» отменителем.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// HTTP-сервер вебхуков. Bot реализует интерфейс webhook.Notifier.
	httpSrv := &http.Server{
		Addr:              cfg.WebhookAddr,
		Handler:           webhook.NewHandler(bot, logger),
		ReadHeaderTimeout: 5 * time.Second, // защита от медленных клиентов (Slowloris)
	}

	var wg sync.WaitGroup

	// Горутина 1 — webhook-сервер.
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("webhook-сервер слушает", "addr", cfg.WebhookAddr)
		// ErrServerClosed — штатное завершение после Shutdown, не ошибка.
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("webhook-сервер упал", "err", err)
			stop() // роняем всё приложение: без вебхуков смысла работать нет
		}
	}()

	// Горутина 2 — Telegram long-polling. Блокируется до отмены ctx.
	wg.Add(1)
	go func() {
		defer wg.Done()
		bot.Run(ctx)
	}()

	// Ждём сигнала завершения (или падения сервера, который вызвал stop()).
	<-ctx.Done()
	logger.Info("получен сигнал завершения — гасим сервисы")

	// Даём HTTP-серверу 10с на до-обработку текущих запросов.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("webhook-сервер не успел закрыться штатно", "err", err)
	}

	wg.Wait() // дожидаемся, пока обе горутины реально завершатся
	return nil
}

// logLevel переводит строку из ENV в уровень slog. По умолчанию — Info.
func logLevel(s string) slog.Level {
	switch s {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "warn", "WARN":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
