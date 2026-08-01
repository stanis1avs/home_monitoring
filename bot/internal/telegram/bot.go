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
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/net/proxy"

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
	// HTTP-клиент собираем отдельно: с SOCKS5-прокси, если он задан, иначе —
	// прямое подключение (как при локальной разработке).
	httpClient, err := buildHTTPClient(cfg.ProxyURL)
	if err != nil {
		return nil, err
	}
	if cfg.ProxyURL != "" {
		log.Info("Telegram через SOCKS5-прокси", "proxy", cfg.ProxyURL)
	} else {
		log.Info("Telegram — прямое подключение (без прокси)")
	}

	api, err := tgbotapi.NewBotAPIWithClient(cfg.TelegramToken, tgbotapi.APIEndpoint, httpClient)
	if err != nil {
		return nil, fmt.Errorf("подключение к Telegram (проверь токен, прокси и доступ к api.telegram.org): %w", err)
	}
	log.Info("подключились к Telegram", "bot", api.Self.UserName)

	return &Bot{api: api, cfg: cfg, prom: prom, docker: docker, log: log}, nil
}

// buildHTTPClient создаёт http.Client для Telegram API.
//
// Если proxyURL пуст — обычный клиент с прямым подключением (транспорт по
// умолчанию). Если задан socks5:// — заворачиваем весь TCP-трафик клиента
// в SOCKS5-прокси (у нас это локальный xray на хосте, туннелирующий на VPS).
//
// Таймаут клиента ОБЯЗАН быть больше pollTimeout: long-polling держит соединение
// открытым до pollTimeout секунд, и при меньшем таймауте каждый опрос обрывался бы.
func buildHTTPClient(proxyURL string) (*http.Client, error) {
	client := &http.Client{Timeout: (pollTimeout + 35) * time.Second}

	if proxyURL == "" {
		return client, nil // прокси не задан — прямое подключение
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("некорректный TELEGRAM_PROXY_URL %q: %w", proxyURL, err)
	}
	// Поддерживаем только SOCKS5 (socks5h = то же самое, но с явным намёком на
	// удалённый резолв — для нас поведение одинаковое, см. ниже про DNS).
	if u.Scheme != "socks5" && u.Scheme != "socks5h" {
		return nil, fmt.Errorf("TELEGRAM_PROXY_URL: поддерживается только socks5://, получено %q", u.Scheme)
	}

	// Логин/пароль к прокси, если заданы в URL (socks5://user:pass@host:port).
	// У локального xray их обычно нет — тогда auth = nil.
	var auth *proxy.Auth
	if u.User != nil {
		password, _ := u.User.Password()
		auth = &proxy.Auth{User: u.User.Username(), Password: password}
	}

	// proxy.SOCKS5 создаёт «диалер»: объект, который вместо прямого соединения
	// сначала подключается к SOCKS5-прокси (u.Host), а затем просит прокси
	// открыть TCP-соединение до нужного адреса. proxy.Direct — базовый диалер,
	// которым обёртка ходит до самого прокси (обычный net.Dial до host:10808).
	dialer, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("не удалось создать SOCKS5-диалер (%s): %w", u.Host, err)
	}

	// http.Transport хочет DialContext(ctx, network, addr) — с поддержкой отмены
	// по контексту. Диалер из x/net/proxy реализует proxy.ContextDialer, поэтому
	// используем его DialContext; на всякий случай оставляем фолбэк на Dial.
	dialContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, network, addr)
		}
		return dialer.Dial(network, addr)
	}

	// ВАЖНО про DNS и IPv6: сюда addr приходит как "api.telegram.org:443" —
	// именно ИМЕНЕМ, http.Transport его не резолвит. SOCKS5-диалер передаёт это
	// имя самому прокси (тип адреса SOCKS5 = domain), и резолвит его xray на той
	// стороне. То есть контейнер вообще НЕ делает DNS-запрос к Telegram — значит
	// проблема «внутри контейнера отдаёт AAAA/IPv6 и виснет» тут не возникает,
	// и принудительный tcp4 не нужен. Единственное прямое соединение — контейнер
	// → host.docker.internal:10808 — идёт через IPv4 (host-gateway из compose).
	client.Transport = &http.Transport{
		DialContext:       dialContext,
		ForceAttemptHTTP2: true, // как у http.DefaultTransport — HTTP/2 к Telegram
	}

	return client, nil
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
