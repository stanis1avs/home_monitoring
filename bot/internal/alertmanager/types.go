// Package alertmanager описывает формат вебхука Alertmanager и умеет
// форматировать его в сообщение для Telegram.
package alertmanager

import "time"

// Payload — тело POST-запроса, которое Alertmanager шлёт на webhook receiver.
// Это стабильный формат "version": "4". Поля названы как в JSON Alertmanager;
// теги json обязательны, т.к. там camelCase, а в Go — экспортируемые имена.
//
// Документация: https://prometheus.io/docs/alerting/latest/configuration/#webhook_config
type Payload struct {
	Version  string `json:"version"`
	GroupKey string `json:"groupKey"`
	// TruncatedAlerts > 0 означает, что Alertmanager обрезал список
	// (при очень больших группах). Покажем это в сообщении.
	TruncatedAlerts int `json:"truncatedAlerts"`
	// Status группы целиком: "firing" пока горит хоть один алерт, иначе "resolved".
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert — один конкретный алерт внутри группы.
type Alert struct {
	// Status отдельного алерта: "firing" или "resolved".
	Status string `json:"status"`
	// Labels — метки из Prometheus (alertname, severity, instance, name, ...).
	Labels map[string]string `json:"labels"`
	// Annotations — человекочитаемое: summary, description.
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	// GeneratorURL — ссылка на график/правило в Prometheus.
	GeneratorURL string `json:"generatorURL"`
	// Fingerprint — стабильный идентификатор алерта (для дедупликации).
	Fingerprint string `json:"fingerprint"`
}

// Вспомогательные геттеры: карта меток/аннотаций может не содержать ключ,
// поэтому обращаемся через методы с дефолтами, а не напрямую по индексу.

func (a Alert) Label(key string) string      { return a.Labels[key] }
func (a Alert) Annotation(key string) string { return a.Annotations[key] }

// Severity возвращает severity алерта (warning/critical/...) или "" если нет.
func (a Alert) Severity() string { return a.Labels["severity"] }

// Name возвращает имя алерта (метка alertname).
func (a Alert) Name() string { return a.Labels["alertname"] }
