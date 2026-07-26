# Telegram-бот мониторинга (Фаза 5)

Go-бот, который:

1. **Принимает вебхуки от Alertmanager** и присылает алерты в Telegram
   (эмодзи по severity, 🔥 firing / ✅ resolved, группировка).
2. **Отвечает на команды** в чате (long-polling, без публичного webhook):
   - `/start`, `/help` — справка;
   - `/status` — какие алерты сейчас горят (Prometheus API);
   - `/top` — топ-5 контейнеров по CPU и RAM (метрики cadvisor);
   - `/ps` — список контейнеров и их статус (Docker API);
   - `/logs <имя>` — последние 30 строк логов контейнера (Docker API);
   - `/restart <имя>` — перезапуск контейнера **с подтверждением кнопками**.

## Архитектура

```
Prometheus ──(алерты)──► Alertmanager ──(webhook JSON)──► bot :9095 /webhook
                                                            │
                                                            ├─► Telegram (сообщения)
                                                            │
Telegram ──(команды, long-polling)──────────────────────► bot
                                                            │
                                                            └─► Docker API (ps/logs/restart)
                                                            └─► Prometheus API (status/top)
```

Структура кода (`internal/`): `config` (ENV), `alertmanager` (типы webhook + форматтер),
`webhook` (HTTP-сервер), `prometheus` (клиент API), `dockerx` (обёртка Docker API),
`telegram` (polling, команды, callbacks). Точка сборки — `main.go`.

## Безопасность (прочитай обязательно)

Бот монтирует `/var/run/docker.sock` — это **полный root-доступ к хосту**.
Единственная защита — **whitelist по `TELEGRAM_CHAT_ID`**: команды от любого
другого чата игнорируются и логируются. Поэтому:

- Никому не давай `TELEGRAM_BOT_TOKEN`.
- Проверь, что `TELEGRAM_CHAT_ID` — именно твой.
- Порт вебхука `9095` наружу хоста **не публикуется** (только сеть `monitoring`).
- Имена контейнеров в `/logs` и `/restart` валидируются по живому списку —
  сырая строка в Docker API не уходит, shell/exec не используется.

---

## Настройка за 4 шага

### 1. Токен бота у @BotFather

1. Открой в Telegram **@BotFather** → `/newbot`.
2. Задай имя и username (должен заканчиваться на `bot`).
3. BotFather пришлёт токен вида `123456789:AAH...`. Это `TELEGRAM_BOT_TOKEN`.

### 2. Свой chat_id

Способ А (проще): напиши боту **@userinfobot** — он ответит твоим `Id`.

Способ Б (без сторонних ботов):
1. Напиши что-нибудь **своему** новому боту в чате (например `привет`).
2. Выполни (подставь токен):
   ```bash
   curl -s "https://api.telegram.org/bot<ТОКЕН>/getUpdates" | grep -o '"chat":{"id":[0-9-]*'
   ```
   Число после `"id":` — это `TELEGRAM_CHAT_ID`.

> В РФ запросы к `api.telegram.org` обычно требуют VPN — и для `curl` выше,
> и для работы самого бота. У тебя Telegram открывается через VPN, так что на
> хосте должен быть доступ к `api.telegram.org` (проверь `curl` выше с хоста N30).

### 3. Заполни .env

```bash
cp bot/.env.example bot/.env
# отредактируй bot/.env: TELEGRAM_BOT_TOKEN и TELEGRAM_CHAT_ID
```
`bot/.env` в git не попадёт (корневой `.gitignore` игнорит `.env`).

### 4. Сборка и запуск

Из корня стека (`~/Desktop/monitoring`):

```bash
# собрать образ бота
docker compose build bot

# поднять новые сервисы
docker compose up -d alertmanager bot

# Prometheus должен перечитать конфиг (добавилась секция alerting):
docker compose restart prometheus
# ...либо без рестарта, если включён lifecycle:
#   curl -X POST http://localhost:9090/-/reload
```

**Если модули не тянутся** (`proxy.golang.org` режется) — собери с зеркалом:
```bash
docker compose build --build-arg GOPROXY=https://goproxy.cn,direct bot
```

---

## Проверка, что всё живо

```bash
docker compose ps            # alertmanager и bot должны быть Up
docker compose logs -f bot   # ищи "подключились к Telegram" и "webhook-сервер слушает"
```

1. **Telegram**: напиши боту `/start` → должна прийти справка. Затем `/ps`, `/status`.
2. **Prometheus видит Alertmanager**: http://<ip-n30>:9090 → Status → Runtime & Build
   Information, либо `curl -s localhost:9090/api/v1/alertmanagers` — там должен быть
   `alertmanager:9093`.
3. **Alertmanager UI**: http://<ip-n30>:9093

### Тестовый алерт (сквозная проверка Alertmanager → бот → Telegram)

Отправь фейковый алерт прямо в Alertmanager (порт 9093 проброшен на хост).
Через несколько секунд (`group_wait`) он прилетит тебе в Telegram, а ещё через
5 минут «погаснет» (`endsAt` в прошлом → resolved):

```bash
curl -XPOST http://localhost:9093/api/v2/alerts -H 'Content-Type: application/json' -d '[
  {
    "labels": {"alertname":"TestAlert","severity":"warning","name":"prometheus"},
    "annotations": {"summary":"Тестовый алерт","description":"Проверка связки Alertmanager → бот"},
    "startsAt": "'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'"
  }
]'
```

Ничего не пришло? Смотри `docker compose logs bot` (там видно `получен webhook`)
и `docker compose logs alertmanager`.

---

## Частые проблемы

| Симптом | Причина / решение |
|---|---|
| Бот не стартует, в логах «не задан TELEGRAM_BOT_TOKEN/CHAT_ID» | Не заполнен `bot/.env`. |
| `подключение к Telegram ... i/o timeout` | Нет доступа к `api.telegram.org` (нужен VPN на хосте). |
| Команды игнорируются, в логах «команда от неразрешённого chat_id» | В `.env` не твой `TELEGRAM_CHAT_ID`. |
| `/top` пустой | cadvisor не отдаёт метрики / имена контейнеров без метки `name`. |
| Алерты не приходят | Prometheus не перечитал конфиг (`restart prometheus`) или не видит `alertmanager:9093`. |
| Сборка падает на `go mod download` | Проблема с GOPROXY — собери с `--build-arg GOPROXY=...` (см. выше). |

## Заметки для обучения

- **Почему long-polling, а не webhook для Telegram** — не нужен публичный HTTPS
  с валидным сертификатом; бот сам ходит наружу. Проще для дома за NAT.
- **Две горутины под общим `context`** (`main.go`): webhook-сервер и polling.
  По `SIGTERM` (его шлёт `docker stop`) контекст отменяется → оба гасятся
  штатно (graceful shutdown).
- **Интерфейс `Notifier`** объявлен в пакете `webhook`, а реализован в `telegram` —
  так `webhook` не зависит от `telegram` (нет циклического импорта). Классический
  для Go приём: «интерфейс у потребителя».
- **`replace` для `go-connections`** в `go.mod` — обход несовместимости версий
  из-за `+incompatible`-модуля docker/docker (подробности — в комментарии в go.mod).
