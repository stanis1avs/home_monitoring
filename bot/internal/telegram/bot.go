// Package telegram — Telegram-бот: long-polling команд и отправка алертов.
//
// Модель работы: НЕ Telegram-webhook, а long-polling (getUpdates). Так проще —
// не нужно публиковать наружу HTTPS-эндпоинт с валидным сертификатом; бот сам
// ходит к api.telegram.org. Для доступа к Telegram из РФ обычно нужен VPN,
// это учтено в README.
package telegram

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"monbot/internal/config"
	"monbot/internal/dockerx"
	"monbot/internal/prometheus"
)

const (
	// tgMaxLen — жёсткий лимит Telegram на длину одного сообщения.
	tgMaxLen = 4096
	// logTail — сколько последних строк логов отдаём по /logs.
	logTail = 30
	// topN — сколько контейнеров показываем в /top.
	topN = 5
	// pollTimeout — таймаут long-polling опроса Telegram (сек).
	pollTimeout = 30
)

// Bot связывает Telegram API с нашими сервисами (Prometheus, Docker).
type Bot struct {
	api    *tgbotapi.BotAPI
	cfg    *config.Config
	prom   *prometheus.Client
	docker *dockerx.Docker
	log    *slog.Logger
}

// New подключается к Telegram (проверяет токен запросом getMe) и собирает Bot.
func New(cfg *config.Config, prom *prometheus.Client, docker *dockerx.Docker, log *slog.Logger) (*Bot, error) {
	// Таймаут HTTP-клиента ОБЯЗАН быть больше pollTimeout: long-polling держит
	// соединение открытым до pollTimeout секунд, и если клиентский таймаут
	// меньше — каждый опрос будет обрываться по таймауту.
	httpClient := &http.Client{Timeout: (pollTimeout + 35) * time.Second}

	api, err := tgbotapi.NewBotAPIWithClient(cfg.TelegramToken, tgbotapi.APIEndpoint, httpClient)
	if err != nil {
		return nil, fmt.Errorf("подключение к Telegram (проверь токен и доступ к api.telegram.org): %w", err)
	}
	log.Info("подключились к Telegram", "bot", api.Self.UserName)

	return &Bot{api: api, cfg: cfg, prom: prom, docker: docker, log: log}, nil
}

// Run запускает цикл обработки апдейтов. Блокируется до отмены ctx
// (graceful shutdown) — тогда останавливает опрос и выходит.
func (b *Bot) Run(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = pollTimeout
	// Просим у Telegram только нужные типы апдейтов — меньше трафика и мусора.
	u.AllowedUpdates = []string{"message", "callback_query"}

	updates := b.api.GetUpdatesChan(u)
	b.log.Info("telegram long-polling запущен")

	for {
		select {
		case <-ctx.Done():
			// StopReceivingUpdates останавливает внутреннюю горутину опроса.
			b.api.StopReceivingUpdates()
			b.log.Info("telegram long-polling остановлен")
			return
		case upd, ok := <-updates:
			if !ok {
				return
			}
			b.handleUpdate(ctx, upd)
		}
	}
}

// handleUpdate маршрутизирует апдейт: команда или нажатие inline-кнопки.
// Обрабатываем последовательно — для одного пользователя этого достаточно,
// и так проще рассуждать о порядке (никаких гонок между командами).
func (b *Bot) handleUpdate(ctx context.Context, upd tgbotapi.Update) {
	switch {
	case upd.Message != nil && upd.Message.IsCommand():
		b.handleCommand(ctx, upd.Message)
	case upd.CallbackQuery != nil:
		b.handleCallback(ctx, upd.CallbackQuery)
	}
}

// authorized — ЕДИНСТВЕННАЯ защита доступа к docker.sock. Пропускаем только
// сообщения из разрешённого чата. Любой другой chat_id — не наш.
func (b *Bot) authorized(chatID int64) bool {
	return chatID == b.cfg.ChatID
}

// Notify реализует webhook.Notifier: доставляет готовый HTML-текст в наш чат.
// Так пакет webhook отправляет алерты, не зная деталей Telegram.
func (b *Bot) Notify(ctx context.Context, htmlText string) error {
	// Уважаем отмену контекста (например, клиент Alertmanager отвалился).
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return b.sendHTML(b.cfg.ChatID, htmlText)
}

// ─────────────────────────── ХЕЛПЕРЫ ОТПРАВКИ ───────────────────────────

// sendHTML отправляет текст в HTML-режиме, разбивая на части по лимиту Telegram.
func (b *Bot) sendHTML(chatID int64, text string) error {
	for _, chunk := range splitMessage(text, tgMaxLen) {
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = tgbotapi.ModeHTML
		msg.DisableWebPagePreview = true
		if _, err := b.api.Send(msg); err != nil {
			return fmt.Errorf("отправка сообщения: %w", err)
		}
	}
	return nil
}

// reply — короткий ответ обычным текстом (без разметки), с логированием ошибки.
func (b *Bot) reply(chatID int64, text string) {
	if err := b.sendHTML(chatID, esc(text)); err != nil {
		b.log.Error("не удалось ответить в чат", "err", err)
	}
}

// splitMessage режет длинный текст на куски <= limit, стараясь рвать по строкам,
// чтобы не разрезать HTML-тег посередине.
func splitMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var parts []string
	var cur strings.Builder
	for _, line := range strings.Split(text, "\n") {
		// Строка длиннее лимита сама по себе — режем жёстко по байтам.
		for len(line) > limit {
			parts = append(parts, line[:limit])
			line = line[limit:]
		}
		if cur.Len()+len(line)+1 > limit {
			parts = append(parts, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// esc экранирует произвольный текст под HTML parse mode Telegram.
func esc(s string) string { return html.EscapeString(s) }

// humanBytes переводит байты в человекочитаемый вид (MB/GB).
func humanBytes(b float64) string {
	const mb = 1024 * 1024
	const gb = 1024 * mb
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GB", b/gb)
	default:
		return fmt.Sprintf("%.0f MB", b/mb)
	}
}
