// Package webhook — HTTP-сервер, принимающий вебхуки от Alertmanager.
//
// Слушает только внутри docker-сети monitoring (порт наружу хоста не
// публикуется — см. docker-compose). Поэтому здесь нет аутентификации:
// достучаться до эндпоинта может только Alertmanager из той же сети.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"monbot/internal/alertmanager"
)

// maxBodyBytes — предел размера тела запроса. Alertmanager шлёт немного,
// но ограничение защищает от случайного/злонамеренного огромного POST.
const maxBodyBytes = 1 << 20 // 1 MiB

// Notifier — то, что умеет доставить готовое сообщение пользователю.
// Интерфейс объявлен здесь (у потребителя), а реализует его пакет telegram.
// Так webhook не зависит от telegram — классический приём развязки в Go.
type Notifier interface {
	Notify(ctx context.Context, htmlText string) error
}

type server struct {
	notifier Notifier
	log      *slog.Logger
}

// NewHandler возвращает http.Handler со всеми маршрутами.
// Паттерны с методом ("POST /webhook") — возможность ServeMux начиная с Go 1.22.
func NewHandler(n Notifier, log *slog.Logger) http.Handler {
	s := &server{notifier: n, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", s.handleWebhook)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return mux
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Ограничиваем размер тела до разбора JSON.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var payload alertmanager.Payload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.log.Warn("не удалось разобрать webhook от alertmanager", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.log.Info("получен webhook",
		"status", payload.Status,
		"alerts", len(payload.Alerts),
		"groupKey", payload.GroupKey,
	)

	text := alertmanager.Format(payload)
	if text == "" {
		// Пустой payload (нет алертов) — просто подтверждаем приём.
		w.WriteHeader(http.StatusOK)
		return
	}

	// Отдельный таймаут на доставку: не завязываемся только на r.Context(),
	// но и уважаем его отмену (клиент разорвал соединение).
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := s.notifier.Notify(ctx, text); err != nil {
		s.log.Error("не удалось отправить алерт в telegram", "err", err)
		// 5xx — Alertmanager повторит доставку позже (у него есть ретраи).
		if errors.Is(err, context.Canceled) {
			http.Error(w, "client closed request", 499)
			return
		}
		http.Error(w, "notify failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
