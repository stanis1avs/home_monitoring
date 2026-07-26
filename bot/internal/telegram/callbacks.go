package telegram

import (
	"context"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Префиксы callback_data для кнопок подтверждения рестарта.
// Формат: "restart:yes:<имя>" и "restart:no".
const (
	callbackConfirm = "restart:yes:"
	callbackCancel  = "restart:no"
)

// handleCallback обрабатывает нажатия inline-кнопок.
func (b *Bot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	// Whitelist и для callback: сообщение с кнопкой должно быть в нашем чате.
	if cb.Message == nil || !b.authorized(cb.Message.Chat.ID) {
		b.log.Warn("callback от неразрешённого источника — игнорирую",
			"from_id", cb.From.ID, "username", cb.From.UserName)
		// Гасим «часики» на кнопке, чтобы у чужого не висела загрузка.
		b.answerCallback(cb.ID, "Недоступно")
		return
	}

	data := cb.Data
	switch {
	case data == callbackCancel:
		b.answerCallback(cb.ID, "Отменено")
		b.editMessage(cb, "❌ Отменено.")

	case strings.HasPrefix(data, callbackConfirm):
		name := strings.TrimPrefix(data, callbackConfirm)
		// Сразу гасим спиннер на кнопке — пользователь видит, что нажатие принято.
		b.answerCallback(cb.ID, "Перезапускаю "+name+"…")
		b.editMessage(cb, "⏳ Перезапускаю <b>"+esc(name)+"</b>…")

		// Рестарт может занять время (graceful stop) — даём отдельный таймаут.
		rctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()

		// Docker-слой сам ещё раз проверит, что контейнер существует.
		if err := b.docker.Restart(rctx, name); err != nil {
			b.log.Error("рестарт не удался", "container", name, "err", err)
			b.editMessage(cb, "⚠️ Не удалось перезапустить <b>"+esc(name)+"</b>:\n"+esc(err.Error()))
			return
		}
		b.log.Info("контейнер перезапущен", "container", name)
		b.editMessage(cb, "✅ Контейнер <b>"+esc(name)+"</b> перезапущен.")

	default:
		// Неизвестный callback — просто гасим спиннер.
		b.answerCallback(cb.ID, "")
	}
}

// answerCallback отвечает на callback (обязательно, иначе на кнопке «крутится»
// индикатор загрузки). Ошибку только логируем — она не критична.
func (b *Bot) answerCallback(id, text string) {
	if _, err := b.api.Request(tgbotapi.NewCallback(id, text)); err != nil {
		b.log.Warn("не смог ответить на callback", "err", err)
	}
}

// editMessage заменяет текст исходного сообщения (с кнопками) на результат.
// Так как ReplyMarkup не задаём — кнопки исчезают, повторно нажать нельзя.
func (b *Bot) editMessage(cb *tgbotapi.CallbackQuery, htmlText string) {
	edit := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, htmlText)
	edit.ParseMode = tgbotapi.ModeHTML
	if _, err := b.api.Send(edit); err != nil {
		b.log.Error("не смог отредактировать сообщение", "err", err)
	}
}
