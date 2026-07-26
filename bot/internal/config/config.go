// Package config читает всю конфигурацию из переменных окружения (12-factor).
//
// Почему именно так: секретов в коде нет и быть не должно — токен бота и
// chat_id приходят только через ENV. Обязательные значения валидируем на
// старте, чтобы бот падал сразу с понятной ошибкой, а не «молча не работал».
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config — вся настройка приложения в одном месте (иммутабельна после Load).
type Config struct {
	// TelegramToken — токен бота от @BotFather. Обязателен.
	TelegramToken string
	// ChatID — единственный разрешённый chat_id (whitelist). Обязателен.
	// Это main line of defense: команды от любого другого чата игнорируются,
	// потому что бот имеет доступ к docker.sock (= root на хосте).
	ChatID int64
	// DockerHost — адрес Docker Engine API. По умолчанию — unix-сокет,
	// который мы монтируем в контейнер.
	DockerHost string
	// WebhookAddr — адрес, на котором слушаем вебхуки от Alertmanager.
	// Внутри docker-сети, наружу хоста не публикуется.
	WebhookAddr string
	// PrometheusURL — база Prometheus HTTP API для команд /status и /top.
	PrometheusURL string
}

// Load собирает Config из окружения и валидирует обязательные поля.
// Возвращает ошибку вместо паники — вызывающий код решит, как реагировать.
func Load() (*Config, error) {
	cfg := &Config{
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		DockerHost:    getEnv("DOCKER_HOST", "unix:///var/run/docker.sock"),
		WebhookAddr:   getEnv("WEBHOOK_LISTEN_ADDR", ":9095"),
		PrometheusURL: getEnv("PROMETHEUS_URL", "http://prometheus:9090"),
	}

	if cfg.TelegramToken == "" {
		return nil, fmt.Errorf("не задан TELEGRAM_BOT_TOKEN")
	}

	// chat_id парсим строго: пустой или мусор — это ошибка конфигурации,
	// а не «разрешить всем». Иначе whitelist можно случайно отключить.
	rawChatID := os.Getenv("TELEGRAM_CHAT_ID")
	if rawChatID == "" {
		return nil, fmt.Errorf("не задан TELEGRAM_CHAT_ID (нужен для whitelist)")
	}
	chatID, err := strconv.ParseInt(rawChatID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("TELEGRAM_CHAT_ID должен быть числом, получено %q: %w", rawChatID, err)
	}
	cfg.ChatID = chatID

	return cfg, nil
}

// getEnv возвращает значение переменной окружения или fallback, если пусто.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
