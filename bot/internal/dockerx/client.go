// Package dockerx — тонкая типизированная обёртка над официальным клиентом
// Docker Engine API. Пакет назван dockerx, чтобы не конфликтовать с именем
// импортируемого пакета docker.
//
// БЕЗОПАСНОСТЬ (критично — мы монтируем docker.sock, а это root на хосте):
//   - никаких shell-команд и exec произвольных строк, только вызовы Docker API;
//   - имя контейнера от пользователя НИКОГДА не подставляется в вызов напрямую —
//     сначала resolveID() ищет его в живом списке контейнеров и возвращает ID.
//     Не нашли в списке — операция отклоняется.
package dockerx

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// Docker — обёртка вокруг *client.Client.
type Docker struct {
	cli *client.Client
}

// Container — сведённое к нужному представление контейнера для команд /ps и /top.
type Container struct {
	Name   string // без ведущего "/"
	State  string // running | exited | paused | ...
	Status string // человекочитаемо: "Up 3 hours", "Exited (0) 5 min ago"
	Image  string
}

// New создаёт клиент к Docker Engine API.
// WithAPIVersionNegotiation: клиент сам согласует версию API с демоном,
// чтобы не падать на «client version is too new».
func New(host string) (*Docker, error) {
	cli, err := client.NewClientWithOpts(
		client.WithHost(host),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("создание docker-клиента: %w", err)
	}
	return &Docker{cli: cli}, nil
}

// Close освобождает ресурсы клиента (вызывается при завершении бота).
func (d *Docker) Close() error { return d.cli.Close() }

// List возвращает все контейнеры (включая остановленные — All: true),
// чтобы /ps показывал и упавшие сервисы.
func (d *Docker) List(ctx context.Context) ([]Container, error) {
	items, err := d.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("список контейнеров: %w", err)
	}

	out := make([]Container, 0, len(items))
	for _, c := range items {
		out = append(out, Container{
			Name:   primaryName(c.Names),
			State:  c.State,
			Status: c.Status,
			Image:  c.Image,
		})
	}
	return out, nil
}

// Logs возвращает последние tail строк логов контейнера (stdout+stderr).
func (d *Docker) Logs(ctx context.Context, name string, tail int) (string, error) {
	id, err := d.resolveID(ctx, name)
	if err != nil {
		return "", err
	}

	reader, err := d.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
		Timestamps: false,
	})
	if err != nil {
		return "", fmt.Errorf("чтение логов %s: %w", name, err)
	}
	defer reader.Close()

	// Docker мультиплексирует stdout и stderr в один поток со служебными
	// заголовками — если у контейнера НЕ включён TTY. stdcopy.StdCopy
	// разбирает этот формат. Если TTY включён, поток «сырой» — читаем как есть.
	var buf strings.Builder
	tty, err := d.hasTTY(ctx, id)
	if err != nil {
		return "", err
	}
	if tty {
		if _, err := io.Copy(&buf, reader); err != nil {
			return "", fmt.Errorf("чтение логов %s: %w", name, err)
		}
	} else {
		// Оба потока сливаем в один буфер — для показа в чате разделять не нужно.
		if _, err := stdcopy.StdCopy(&buf, &buf, reader); err != nil {
			return "", fmt.Errorf("демультиплексирование логов %s: %w", name, err)
		}
	}
	return buf.String(), nil
}

// Restart перезапускает контейнер. Timeout — сколько ждать graceful stop
// перед kill (10с — разумно для домашних сервисов).
func (d *Docker) Restart(ctx context.Context, name string) error {
	id, err := d.resolveID(ctx, name)
	if err != nil {
		return err
	}
	timeout := 10
	if err := d.cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("рестарт %s: %w", name, err)
	}
	return nil
}

// resolveID — сердце валидации. Принимает имя от пользователя и ищет точное
// совпадение среди РЕАЛЬНЫХ контейнеров. Возвращает ID (его и передаём в API).
// Так сырая пользовательская строка никогда не доходит до Docker как цель.
func (d *Docker) resolveID(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("не указано имя контейнера")
	}

	items, err := d.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", fmt.Errorf("список контейнеров: %w", err)
	}
	for _, c := range items {
		if primaryName(c.Names) == name {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("контейнер %q не найден", name)
}

// primaryName берёт первое имя контейнера и убирает ведущий слэш
// (Docker отдаёт имена как "/immich_server").
func primaryName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// hasTTY инспектирует контейнер, чтобы понять, мультиплексирован ли лог-поток.
func (d *Docker) hasTTY(ctx context.Context, id string) (bool, error) {
	info, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return false, fmt.Errorf("inspect контейнера: %w", err)
	}
	return info.Config.Tty, nil
}
