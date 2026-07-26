// Package prometheus — тонкий клиент к Prometheus HTTP API.
//
// Нужен для двух команд бота:
//   - /status — какие алерты сейчас в состоянии firing (GET /api/v1/alerts);
//   - /top    — топ контейнеров по CPU и RAM (instant-запросы к /api/v1/query).
//
// Специально не тянем официальную клиентскую библиотеку Prometheus: нам нужны
// два эндпоинта, а она тащит много зависимостей. Свой клиент на net/http —
// нагляднее для обучения и легче по зависимостям.
package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client — HTTP-клиент к одному инстансу Prometheus.
type Client struct {
	baseURL string
	http    *http.Client
}

// New создаёт клиент. Таймаут задаём на самом http.Client — это «страховка»
// на случай, если context забудут ограничить по времени.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// FiringAlert — упрощённое представление одного горящего алерта для /status.
type FiringAlert struct {
	Name     string
	Severity string
	Summary  string
	ActiveAt time.Time
}

// Sample — одна строка результата instant-запроса: имя (метка name) и значение.
type Sample struct {
	Name  string
	Value float64
}

// FiringAlerts возвращает алерты в состоянии firing.
// Prometheus в /api/v1/alerts отдаёт и pending, и firing — фильтруем сами.
func (c *Client) FiringAlerts(ctx context.Context) ([]FiringAlert, error) {
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Alerts []struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				State       string            `json:"state"`
				ActiveAt    time.Time         `json:"activeAt"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/api/v1/alerts", nil, &resp); err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus вернул status=%q", resp.Status)
	}

	var out []FiringAlert
	for _, a := range resp.Data.Alerts {
		if a.State != "firing" {
			continue // pending нам не интересен — он ещё «созревает»
		}
		out = append(out, FiringAlert{
			Name:     a.Labels["alertname"],
			Severity: a.Labels["severity"],
			Summary:  a.Annotations["summary"],
			ActiveAt: a.ActiveAt,
		})
	}
	return out, nil
}

// TopByCPU — топ-n контейнеров по загрузке CPU (доля ядра, где 1.0 = одно ядро).
// rate за 2 минуты сглаживает пики; sum by (name) объединяет cgroup-строки
// одного контейнера в одну.
func (c *Client) TopByCPU(ctx context.Context, n int) ([]Sample, error) {
	q := fmt.Sprintf(`topk(%d, sum by (name) (rate(container_cpu_usage_seconds_total{name!=""}[2m])))`, n)
	return c.query(ctx, q)
}

// TopByMemory — топ-n контейнеров по использованию RAM в байтах.
func (c *Client) TopByMemory(ctx context.Context, n int) ([]Sample, error) {
	q := fmt.Sprintf(`topk(%d, sum by (name) (container_memory_usage_bytes{name!=""}))`, n)
	return c.query(ctx, q)
}

// query выполняет instant-запрос и возвращает вектор, отсортированный по
// убыванию значения (topk не гарантирует порядок в JSON-ответе).
func (c *Client) query(ctx context.Context, promQL string) ([]Sample, error) {
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				// value = [<unix ts float>, "<значение строкой>"].
				// Второй элемент Prometheus всегда шлёт строкой — держим
				// как RawMessage и парсим вручную.
				Value [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/api/v1/query", url.Values{"query": {promQL}}, &resp); err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus вернул status=%q", resp.Status)
	}

	out := make([]Sample, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		// Значение приходит как JSON-строка ("123.4") — снимаем кавычки и парсим.
		var raw string
		if err := json.Unmarshal(r.Value[1], &raw); err != nil {
			continue // битую строку просто пропускаем, не роняем всю команду
		}
		val, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		name := r.Metric["name"]
		if name == "" {
			name = "(без имени)"
		}
		out = append(out, Sample{Name: name, Value: val})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out, nil
}

// getJSON выполняет GET и декодирует JSON в target. Контекст обязателен —
// через него работают и таймаут, и отмена при завершении бота.
func (c *Client) getJSON(ctx context.Context, path string, params url.Values, target any) error {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("сборка запроса к prometheus: %w", err)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("запрос к prometheus: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus ответил %s на %s", res.Status, path)
	}

	if err := json.NewDecoder(res.Body).Decode(target); err != nil {
		return fmt.Errorf("разбор ответа prometheus: %w", err)
	}
	return nil
}
