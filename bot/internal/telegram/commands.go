package telegram

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleCommand — точка входа для текстовых команд. Сначала whitelist, потом роутинг.
func (b *Bot) handleCommand(ctx context.Context, msg *tgbotapi.Message) {
	if !b.authorized(msg.Chat.ID) {
		// Логируем попытку — это может быть чужой, наткнувшийся на бота.
		b.log.Warn("команда от неразрешённого chat_id — игнорирую",
			"chat_id", msg.Chat.ID,
			"username", msg.From.UserName,
			"cmd", msg.Command(),
		)
		return
	}

	cmd := msg.Command()
	args := strings.TrimSpace(msg.CommandArguments())
	b.log.Info("команда", "cmd", cmd, "args", args)

	switch cmd {
	case "start", "help":
		b.cmdHelp(msg.Chat.ID)
	case "status":
		b.cmdStatus(ctx, msg.Chat.ID)
	case "top":
		b.cmdTop(ctx, msg.Chat.ID)
	case "ps":
		b.cmdPS(ctx, msg.Chat.ID)
	case "logs":
		b.cmdLogs(ctx, msg.Chat.ID, args)
	case "restart":
		b.cmdRestart(ctx, msg.Chat.ID, args)
	default:
		b.reply(msg.Chat.ID, "Неизвестная команда. /help — список.")
	}
}

// cmdHelp — /start и /help.
func (b *Bot) cmdHelp(chatID int64) {
	text := strings.Join([]string{
		"<b>🤖 Бот мониторинга N30</b>",
		"",
		"Команды:",
		"/status — какие алерты сейчас горят",
		"/top — топ контейнеров по CPU и RAM",
		"/ps — список контейнеров и их статус",
		"/logs &lt;имя&gt; — последние строки логов контейнера",
		"/restart &lt;имя&gt; — перезапуск контейнера (с подтверждением)",
		"/help — эта справка",
		"",
		"<i>Имя контейнера подсмотри в /ps.</i>",
	}, "\n")
	if err := b.sendHTML(chatID, text); err != nil {
		b.log.Error("не смог отправить help", "err", err)
	}
}

// cmdStatus — идём в Prometheus за firing-алертами.
func (b *Bot) cmdStatus(ctx context.Context, chatID int64) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	alerts, err := b.prom.FiringAlerts(ctx)
	if err != nil {
		b.reply(chatID, "⚠️ Не смог опросить Prometheus: "+err.Error())
		return
	}
	if len(alerts) == 0 {
		b.reply(chatID, "Всё спокойно ✅")
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🔥 <b>Горящих алертов: %d</b>\n", len(alerts))
	for _, a := range alerts {
		emoji := severityEmoji(a.Severity)
		fmt.Fprintf(&sb, "\n%s <b>%s</b>\n", emoji, esc(a.Name))
		if a.Summary != "" {
			fmt.Fprintf(&sb, "%s\n", esc(a.Summary))
		}
		if !a.ActiveAt.IsZero() {
			fmt.Fprintf(&sb, "🕒 с %s\n", a.ActiveAt.Local().Format("02.01 15:04"))
		}
	}
	if err := b.sendHTML(chatID, strings.TrimRight(sb.String(), "\n")); err != nil {
		b.log.Error("не смог отправить status", "err", err)
	}
}

// cmdTop — топ контейнеров по CPU и RAM из метрик cadvisor.
func (b *Bot) cmdTop(ctx context.Context, chatID int64) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var sb strings.Builder

	cpu, err := b.prom.TopByCPU(ctx, topN)
	if err != nil {
		b.reply(chatID, "⚠️ Не смог получить CPU из Prometheus: "+err.Error())
		return
	}
	sb.WriteString("🔥 <b>Топ по CPU</b> (% одного ядра)\n")
	for i, s := range cpu {
		// value — доля ядра (1.0 = 100% одного ядра). Умножаем на 100 для процентов.
		fmt.Fprintf(&sb, "%d. <code>%s</code> — %.1f%%\n", i+1, esc(s.Name), s.Value*100)
	}

	mem, err := b.prom.TopByMemory(ctx, topN)
	if err != nil {
		b.reply(chatID, "⚠️ Не смог получить RAM из Prometheus: "+err.Error())
		return
	}
	sb.WriteString("\n💾 <b>Топ по RAM</b>\n")
	for i, s := range mem {
		fmt.Fprintf(&sb, "%d. <code>%s</code> — %s\n", i+1, esc(s.Name), humanBytes(s.Value))
	}

	if err := b.sendHTML(chatID, strings.TrimRight(sb.String(), "\n")); err != nil {
		b.log.Error("не смог отправить top", "err", err)
	}
}

// cmdPS — список контейнеров через Docker API.
func (b *Bot) cmdPS(ctx context.Context, chatID int64) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	list, err := b.docker.List(ctx)
	if err != nil {
		b.reply(chatID, "⚠️ Не смог получить список контейнеров: "+err.Error())
		return
	}
	if len(list) == 0 {
		b.reply(chatID, "Контейнеров не найдено.")
		return
	}

	// Сортируем: сначала запущенные, потом по имени — стабильный вывод.
	sort.Slice(list, func(i, j int) bool {
		if (list[i].State == "running") != (list[j].State == "running") {
			return list[i].State == "running"
		}
		return list[i].Name < list[j].Name
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "📦 <b>Контейнеры (%d)</b>\n", len(list))
	for _, c := range list {
		fmt.Fprintf(&sb, "\n%s <code>%s</code>\n<i>%s</i>\n",
			stateEmoji(c.State), esc(c.Name), esc(c.Status))
	}
	if err := b.sendHTML(chatID, strings.TrimRight(sb.String(), "\n")); err != nil {
		b.log.Error("не смог отправить ps", "err", err)
	}
}

// cmdLogs — последние строки логов контейнера через Docker API.
func (b *Bot) cmdLogs(ctx context.Context, chatID int64, name string) {
	if name == "" {
		b.reply(chatID, "Использование: /logs <имя_контейнера>\nСписок имён — /ps")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	logs, err := b.docker.Logs(ctx, name, logTail)
	if err != nil {
		// Ошибка «не найден» приходит отсюда — безопасно показать пользователю.
		b.reply(chatID, "⚠️ "+err.Error())
		return
	}
	logs = strings.TrimSpace(logs)
	if logs == "" {
		b.reply(chatID, "Логи пусты.")
		return
	}

	// Оставляем место под обёртку <pre> и заголовок в пределах лимита Telegram.
	const budget = tgMaxLen - 200
	if len(logs) > budget {
		logs = "…(обрезано)\n" + logs[len(logs)-budget:]
	}

	// <pre> — моноширинный блок; содержимое обязательно экранируем.
	text := fmt.Sprintf("📜 <b>%s</b> (последние %d строк)\n<pre>%s</pre>",
		esc(name), logTail, esc(logs))
	if err := b.sendHTML(chatID, text); err != nil {
		b.log.Error("не смог отправить логи", "err", err)
	}
}

// cmdRestart — НЕ рестартит сразу: сначала проверяет, что контейнер существует,
// затем показывает inline-кнопки подтверждения. Сам рестарт — в callbacks.go.
func (b *Bot) cmdRestart(ctx context.Context, chatID int64, name string) {
	if name == "" {
		b.reply(chatID, "Использование: /restart <имя_контейнера>\nСписок имён — /ps")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Проверяем существование ДО показа кнопки — чтобы не предлагать рестарт
	// несуществующего контейнера. На самом рестарте (callback) проверим ещё раз.
	list, err := b.docker.List(ctx)
	if err != nil {
		b.reply(chatID, "⚠️ Не смог проверить контейнер: "+err.Error())
		return
	}
	found := false
	for _, c := range list {
		if c.Name == name {
			found = true
			break
		}
	}
	if !found {
		b.reply(chatID, fmt.Sprintf("⚠️ Контейнер %q не найден. Список — /ps", name))
		return
	}

	// callback_data ограничен 64 байтами — имена контейнеров короткие, влезают.
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Да, рестартнуть "+name, callbackConfirm+name),
			tgbotapi.NewInlineKeyboardButtonData("❌ Отмена", callbackCancel),
		),
	)
	m := tgbotapi.NewMessage(chatID, fmt.Sprintf("Точно перезапустить <b>%s</b>?", esc(name)))
	m.ParseMode = tgbotapi.ModeHTML
	m.ReplyMarkup = kb
	if _, err := b.api.Send(m); err != nil {
		b.log.Error("не смог отправить подтверждение рестарта", "err", err)
	}
}

// severityEmoji — эмодзи по уровню важности алерта (для /status).
func severityEmoji(sev string) string {
	switch strings.ToLower(sev) {
	case "critical":
		return "🔴"
	case "warning":
		return "🟡"
	case "info":
		return "🔵"
	default:
		return "⚪"
	}
}

// stateEmoji — цветовой индикатор статуса контейнера для /ps.
func stateEmoji(state string) string {
	switch state {
	case "running":
		return "🟢"
	case "exited", "dead":
		return "🔴"
	case "paused":
		return "⏸"
	case "restarting":
		return "🔄"
	default:
		return "⚪"
	}
}
