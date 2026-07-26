package alertmanager

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// Format превращает Payload в готовое HTML-сообщение для Telegram.
//
// Почему HTML, а не Markdown: значения (summary/description/имена) содержат
// произвольные символы из метрик и логов. В HTML их достаточно прогнать через
// html.EscapeString — и парсер Telegram не сломается. В Markdown же пришлось бы
// экранировать целый набор спецсимволов вручную и легко ошибиться.
//
// Алерты группируются на «горящие» и «погашенные», внутри — сортировка по
// severity (critical выше warning), чтобы важное было сверху.
func Format(p Payload) string {
	var b strings.Builder

	// Разделяем на firing/resolved — их визуально удобно показывать раздельно.
	var firing, resolved []Alert
	for _, a := range p.Alerts {
		if a.Status == "resolved" {
			resolved = append(resolved, a)
		} else {
			firing = append(firing, a)
		}
	}

	sortBySeverity(firing)
	sortBySeverity(resolved)

	// Шапка: одной строкой суть — сколько горит / погасло.
	switch {
	case len(firing) > 0 && len(resolved) > 0:
		fmt.Fprintf(&b, "🔥 <b>%d firing</b>, ✅ %d resolved\n", len(firing), len(resolved))
	case len(firing) > 0:
		fmt.Fprintf(&b, "🔥 <b>Сработало алертов: %d</b>\n", len(firing))
	case len(resolved) > 0:
		fmt.Fprintf(&b, "✅ <b>Погашено алертов: %d</b>\n", len(resolved))
	}

	for _, a := range firing {
		writeAlert(&b, a, true)
	}
	for _, a := range resolved {
		writeAlert(&b, a, false)
	}

	if p.TruncatedAlerts > 0 {
		fmt.Fprintf(&b, "\n<i>…и ещё %d алертов (обрезано Alertmanager)</i>", p.TruncatedAlerts)
	}

	return strings.TrimRight(b.String(), "\n")
}

// writeAlert дописывает в builder блок одного алерта.
func writeAlert(b *strings.Builder, a Alert, firing bool) {
	statusEmoji := "✅"
	if firing {
		statusEmoji = "🔥"
	}

	// Заголовок алерта: [эмодзи severity][эмодзи статуса] <b>Имя</b>
	fmt.Fprintf(b, "\n%s%s <b>%s</b>\n", severityEmoji(a.Severity()), statusEmoji, esc(a.Name()))

	// summary — короткая суть. description — подробности (может отсутствовать).
	if s := a.Annotation("summary"); s != "" {
		fmt.Fprintf(b, "%s\n", esc(s))
	}
	if d := a.Annotation("description"); d != "" {
		fmt.Fprintf(b, "<i>%s</i>\n", esc(d))
	}

	// «Где»: показываем самую конкретную метку, что есть у этого алерта.
	if where := targetLabel(a); where != "" {
		fmt.Fprintf(b, "📍 <code>%s</code>\n", esc(where))
	}

	// Время начала — в локальной зоне сервера (TZ задаётся через ENV в compose,
	// tzdata вшита в бинарь, см. main.go).
	if !a.StartsAt.IsZero() {
		fmt.Fprintf(b, "🕒 %s\n", a.StartsAt.Local().Format("02.01 15:04:05"))
	}
}

// severityEmoji подбирает эмодзи по уровню важности.
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

// targetLabel выбирает наиболее конкретную «где сломалось» метку.
// Порядок приоритета: контейнер → инстанс → устройство → точка монтирования.
func targetLabel(a Alert) string {
	for _, key := range []string{"name", "instance", "device", "mountpoint"} {
		if v := a.Label(key); v != "" {
			return fmt.Sprintf("%s=%s", key, v)
		}
	}
	return ""
}

// severityRank задаёт порядок сортировки: меньше число — выше в сообщении.
func severityRank(sev string) int {
	switch strings.ToLower(sev) {
	case "critical":
		return 0
	case "warning":
		return 1
	case "info":
		return 2
	default:
		return 3
	}
}

// sortBySeverity сортирует алерты по важности, затем по имени — стабильный
// предсказуемый вывод.
func sortBySeverity(alerts []Alert) {
	sort.SliceStable(alerts, func(i, j int) bool {
		ri, rj := severityRank(alerts[i].Severity()), severityRank(alerts[j].Severity())
		if ri != rj {
			return ri < rj
		}
		return alerts[i].Name() < alerts[j].Name()
	})
}

// esc — короткий алиас для экранирования значений под HTML parse mode.
func esc(s string) string { return html.EscapeString(s) }
