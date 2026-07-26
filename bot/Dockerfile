# syntax=docker/dockerfile:1

# ─────────────────────────── ЭТАП СБОРКИ ───────────────────────────
# golang:1.25 — потому что go.mod требует go 1.25 (свежие транзитивные модули).
# alpine-вариант меньше и его образ уже с Docker Hub (второй реестр не нужен).
FROM golang:1.25-alpine AS builder

# ca-certificates — для HTTPS при go mod download И чтобы скопировать бандл
# в финальный scratch-образ (иначе бот не проверит TLS к api.telegram.org).
RUN apk add --no-cache ca-certificates

WORKDIR /src

# GOPROXY можно переопределить при сборке под РФ, если proxy.golang.org режется:
#   docker compose build --build-arg GOPROXY=https://goproxy.cn,direct
# По умолчанию — стандартный проксик.
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

# Сначала только манифесты — слой с зависимостями закешируется и не будет
# качаться заново при каждой правке кода (классический приём слоёв Docker).
COPY go.mod go.sum ./
RUN go mod download

# Теперь исходники.
COPY . .

# CGO_ENABLED=0 → полностью статический бинарь, поедет на пустой scratch.
# -trimpath убирает абсолютные пути сборки; -ldflags "-s -w" срезает отладочную
# информацию → итоговый бинарь заметно меньше.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /monbot .

# ─────────────────────────── ФИНАЛЬНЫЙ ОБРАЗ ───────────────────────────
# scratch — абсолютно пустой образ. Ничего лишнего = минимум поверхности атаки
# и вес ~15-20 МБ. tzdata вшита в бинарь (import _ "time/tzdata"), поэтому
# зону копировать не нужно — хватит переменной TZ.
FROM scratch

# Корневые сертификаты для валидации TLS (Telegram).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /monbot /monbot

# ВАЖНО про пользователя: образ работает от root (uid 0). Это осознанный выбор —
# нужен доступ к смонтированному /var/run/docker.sock (он root:docker на хосте),
# а жёсткая привязка к GID группы docker хрупка (GID отличается между хостами).
# Реальная защита здесь — whitelist по TELEGRAM_CHAT_ID в самом боте, а не uid.

# Документируем порт вебхука. НАРУЖУ хоста он не публикуется (см. docker-compose).
EXPOSE 9095

ENTRYPOINT ["/monbot"]
