package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/go-faster/errors"
)

const maxVisibleTasks = 30

type storedTask struct {
	URL        string    `json:"url"`
	State      taskState `json:"state"`
	Detail     string    `json:"detail,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

func (d *Desktop) loadTasks() error {
	b, err := os.ReadFile(filepath.Join(d.stateDir, "tasks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "read task history")
	}
	var stored []storedTask
	if err := json.Unmarshal(b, &stored); err != nil {
		return errors.Wrap(err, "parse task history")
	}
	d.tasks = make([]*task, 0, len(stored))
	for _, item := range stored {
		if item.State == taskRunning || item.State == taskWaiting {
			item.State = taskCanceled
			item.Detail = d.tr("interrupted")
			item.FinishedAt = time.Now()
		}
		d.tasks = append(d.tasks, &task{url: item.URL, state: item.State, detail: item.Detail, createdAt: item.CreatedAt, finishedAt: item.FinishedAt})
	}
	return nil
}

func (d *Desktop) saveTasks() error {
	d.mu.Lock()
	stored := make([]storedTask, 0, len(d.tasks))
	for _, t := range d.tasks {
		stored = append(stored, storedTask{URL: t.url, State: t.state, Detail: t.detail, CreatedAt: t.createdAt, FinishedAt: t.finishedAt})
	}
	d.mu.Unlock()
	b, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.stateDir, "tasks.json"), b, 0o600)
}

func (d *Desktop) visibleTaskCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.tasks) > maxVisibleTasks {
		return maxVisibleTasks
	}
	return len(d.tasks)
}

func (d *Desktop) taskForVisibleID(id int) *task {
	d.mu.Lock()
	defer d.mu.Unlock()
	index := len(d.tasks) - 1 - id
	if index < 0 || index >= len(d.tasks) {
		return nil
	}
	return d.tasks[index]
}
