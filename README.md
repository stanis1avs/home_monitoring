# Home Monitoring — мониторинг домашнего сервера N30

Self-hosted observability-стек для домашнего мини-ПК (Intel N30, Ubuntu):
железо, диски, контейнеры и доступность сервисов — с алертами и управлением
прямо из Telegram.

## О проекте

**Home Monitoring** — лёгкий стек мониторинга на готовых Go-компонентах
(Prometheus + экспортеры) плюс **собственный Telegram-бот на Go**, который
доставляет алерты и позволяет управлять контейнерами из чата.

### Что отслеживается:
- 💾 **Диски** — свободное место, прогноз заполнения (`predict_linear`), SMART (рост бэд-секторов, температура)
- 🌡️ **Температура и троттлинг** CPU (hwmon)
- 🎮 **iGPU / QuickSync** — загрузка видео-движка (риск буферинга при транскоде в Jellyfin)
- 🧠 **RAM и swap** — детект активного свопинга
- 🌐 **Доступность приложений** — Immich, Navidrome, Jellyfin (blackbox-пробы)
- 📦 **Контейнеры** — OOM-kill, рестарт-циклы, потребление CPU/RAM (cadvisor)

### Алерты в Telegram:
Трёхзвенный конвейер доставки:
- **Prometheus** вычисляет правила из `prometheus/rules/alerts.yml`
- **Alertmanager** группирует, дедуплицирует и шлёт вебхуком боту
- **Go-бот** форматирует и присылает в чат: эмодзи по severity (🔴 critical / 🟡 warning), 🔥 firing / ✅ resolved, группировка нескольких алертов в одно сообщение

### Управление из чата:
Собственный бот на Go (long-polling, без публичного webhook) отвечает на команды:
- `/status` — какие алерты сейчас горят (Prometheus API)
- `/top` — топ-5 контейнеров по CPU и RAM (метрики cadvisor)
- `/ps` — список контейнеров и их статусы (Docker API)
- `/logs <имя>` — последние 30 строк логов контейнера (Docker API)
- `/restart <имя>` — перезапуск контейнера **с подтверждением inline-кнопками**

**Безопасность:** бот монтирует `docker.sock` (= root на хосте), поэтому защита
серьёзная — whitelist по `TELEGRAM_CHAT_ID` (команды от любого другого чата
игнорируются), валидация имён контейнеров по живому списку, никаких shell/exec,
порт вебхука не публикуется наружу. Подробности — в [bot/README.md](bot/README.md).

---

## Технический стэк

### Ядро
1. **Prometheus** `v3.1.0` — сбор метрик, вычисление правил алертов, TSDB (ретеншн 30д / 8ГБ)
2. **Grafana** `11.4.0` — дашборды (provisioning датасорса из коробки)
3. **Alertmanager** `v0.28.0` — маршрутизация, группировка и дедупликация алертов

### Экспортеры (сбор метрик)
1. **node-exporter** `v1.8.2` — железо: диски, память, температуры (hwmon), vmstat
2. **cadvisor** `v0.49.1` — метрики контейнеров (CPU, RAM, OOM, рестарты)
3. **smartctl-exporter** `v0.13.0` — SMART/здоровье физических дисков
4. **intel-gpu-exporter** (`restreamio/intel-prometheus`) — загрузка iGPU / QuickSync
5. **blackbox-exporter** `v0.26.0` — HTTP-пробы доступности приложений

### Telegram-бот (свой код на Go, `./bot`)
1. **Go 1.25** — модуль `monbot`, аккуратная разбивка по пакетам (`config`, `alertmanager`, `webhook`, `prometheus`, `dockerx`, `telegram`)
2. **go-telegram-bot-api/v5** — Telegram Bot API: long-polling + inline-кнопки
3. **docker/docker client** — типизированный доступ к Docker Engine API (ps / logs / restart)
4. **net/http + log/slog** — webhook-сервер для Alertmanager, структурные логи
5. **context.Context везде** — таймауты и graceful shutdown по SIGTERM/SIGINT (2 горутины под общим контекстом)
6. **multi-stage Docker build** → финальный образ `scratch` (~15–20 МБ, статический бинарь + CA-сертификаты)

### Инфраструктура
- **Docker Compose** — весь стек в одной сети `monitoring`, конфиг через ENV (12-factor)
- **Секреты** — только в `.env` (в git не попадают), никаких токенов в коде

---

## Быстрый старт

```bash
cp .env.example .env              # пароль Grafana
cp bot/.env.example bot/.env      # TELEGRAM_BOT_TOKEN и TELEGRAM_CHAT_ID (см. bot/README.md)

docker compose build bot          # собрать бота (при проблемах с proxy см. bot/README.md)
docker compose up -d              # поднять весь стек
docker compose ps                 # все должны быть Up
```

- Grafana:      http://<ip-n30>:3000 (admin / пароль из `.env`)
- Prometheus:   http://<ip-n30>:9090 (Status → Targets — все UP)
- Alertmanager: http://<ip-n30>:9093
- Алерты:       http://<ip-n30>:9090/alerts

Настройка Telegram-бота с нуля (BotFather, chat_id, тестовый алерт) — в [bot/README.md](bot/README.md).

## Дашборды Grafana (импорт по ID)

Grafana → Dashboards → New → Import → вставь ID → выбери Prometheus:
- **1860** — Node Exporter Full (железо, диски, память, температура)
- **19792** / **14282** — cadvisor / контейнеры
- **13639** — smartctl-exporter
- **7587** — Blackbox Exporter (доступность)

## Что подправить под себя

1. **Порты приложений** в `prometheus/prometheus.yml` (блок `blackbox-http`): Immich 2283, Navidrome, Jellyfin 8096.
2. **Пороги алертов** в `prometheus/rules/alerts.yml` — подобраны разумно, но темп CPU (>90°C), порог свопа и т.д. стоит подогнать под своё железо.
3. **Таймзона бота** — `TZ` в сервисе `bot` (по умолчанию `Europe/Moscow`).

После правки конфига Prometheus — перечитать без рестарта:
```bash
curl -X POST http://localhost:9090/-/reload
```

## Оговорки (честно)

- **intel-gpu-exporter — самый капризный.** Community-образы меняются; проверь эндпоинт после старта:
  `docker exec -it intel-gpu-exporter wget -qO- localhost:8080/metrics | head`. Если не заводится — выключи, остальной стек от него не зависит.
- **Имена SMART-метрик** зависят от версии экспортера и типа диска (ATA vs NVMe). Молчит алерт — глянь реальные метрики (`smartctl_...`) в Prometheus UI.
- **`container_oom_events_total`** есть не во всех сборках cadvisor — проверь, если алерт по OOM не срабатывает.
- **Telegram в РФ** — боту на N30 нужен доступ к `api.telegram.org` (обычно через VPN на самом хосте). Проверка: `curl -s -o /dev/null -w "%{http_code}" https://api.telegram.org` должен вернуть `200`.

---

## В разработке / идеи

- `inhibit_rules` в Alertmanager (гасить warning, если по тому же инстансу горит critical)
- Silence-команды бота (`/silence <alert> <длительность>`) через Alertmanager API
- Healthcheck для сервиса `bot` (сейчас нет — образ `scratch` без shell)
- Дашборд-скриншоты в README
- Бэкап Grafana/Prometheus на внешний диск
