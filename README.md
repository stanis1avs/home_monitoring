# Мониторинг домашнего N30 — Фазы 0-3

Лёгкий observability-стек: всё из готовых Go-компонентов, свой код появится
на Фазе 5 (Alertmanager → Go-бот в Telegram).

## Быстрый старт

```bash
cp .env.example .env          # поменяй пароль Grafana
docker compose up -d
docker compose ps             # все должны быть healthy/running
```

- Grafana:    http://<ip-n30>:3000   (admin / пароль из .env)
- Prometheus: http://<ip-n30>:9090   (вкладка Status → Targets — все должны быть UP)
- Алерты:     http://<ip-n30>:9090/alerts

## Что где мониторится (маппинг 6 пунктов)

| # | Что                     | Кто собирает            |
|---|-------------------------|-------------------------|
| 1 | Диск: место, I/O, темп  | node-exporter + smartctl-exporter (SMART) |
| 2 | Тепло / троттлинг        | node-exporter (hwmon)   |
| 3 | iGPU / QuickSync         | intel-gpu-exporter      |
| 4 | RAM / swap               | node-exporter (vmstat)  |
| 5 | Доступность приложений   | blackbox-exporter       |
| 6 | Контейнеры               | cadvisor                |

## Дашборды (импорт по ID)

Grafana → Dashboards → New → Import → вставь ID → выбери Prometheus:
- **1860** — Node Exporter Full (железо, диски, память, темп)
- **19792** или **14282** — cadvisor / контейнеры
- **13639** — smartctl-exporter (если версия совпадёт)
- **7587** — Blackbox Exporter (доступность)

## Что подправить под себя

1. **Порты приложений** в `prometheus/prometheus.yml` (блок `blackbox-http`):
   по умолчанию Immich 2283, Navidrome 4533, Jellyfin 8096.
2. **Пороги алертов** в `prometheus/rules/alerts.yml` — подобраны разумно,
   но темп CPU (>90°C), порог свопа и т.д. стоит подогнать под своё железо.

После правки конфига — перечитать без рестарта:
```bash
curl -X POST http://localhost:9090/-/reload
```

## Важные оговорки (честно)

- **Alertmanager здесь НЕТ** — это Фаза 5. Правила уже работают и видны
  в Prometheus UI (/alerts), но никуда не доставляются, пока не поднимешь
  Alertmanager + Go-бот.
- **intel-gpu-exporter — самый капризный.** Community-образы меняются.
  Проверь эндпоинт после старта:
  ```bash
  docker exec -it intel-gpu-exporter wget -qO- localhost:8080/metrics | head
  ```
  Узнаешь реальное имя метрики загрузки — подставь в закомментированный
  алерт `IgpuSaturated` в alerts.yml. Если образ вообще не заводится —
  выключи сервис, остальной стек от него не зависит.
- **Имена SMART-метрик** зависят от версии экспортера и типа диска
  (ATA vs NVMe). Если SMART-алерт молчит — глянь реальные метрики в
  Prometheus UI (начни печатать `smartctl_`) и поправь имена в правиле.
- **container_oom_events_total** есть не во всех сборках cadvisor —
  проверь так же, если алерт по OOM не срабатывает.

## Расход ресурсов

Весь стек ~1.5-2 ГБ RAM. Ретеншн TSDB уже ограничен (30 дней / 8 ГБ)
флагами в compose, чтобы Prometheus не забил диск, за которым следит.
Если бокс на 8 ГБ и тесно — подними scrape_interval до 60с в prometheus.yml.
