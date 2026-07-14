// Package desktop provides the Fyne based desktop application.
package desktop

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/iyear/tdl/app/dl"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	tclientcore "github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/core/util/logutil"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/kv"
)

const accountNamespace = "desktop-account"

var Version = "dev"

//go:embed assets/icon.png
var iconPNG []byte

type Settings struct {
	Language    string `json:"language"`
	AppID       int    `json:"app_id"`
	AppHash     string `json:"app_hash"`
	DownloadDir string `json:"download_dir"`
	Proxy       string `json:"proxy"`
	Tasks       int    `json:"tasks"`
	Threads     int    `json:"threads"`
	PoolSize    int    `json:"pool_size"`
}

func defaultSettings() Settings {
	home, _ := os.UserHomeDir()
	return Settings{Language: "zh", DownloadDir: filepath.Join(home, "Downloads", "Telegram Downloads"), Tasks: 2, Threads: 4, PoolSize: 8}
}

type taskState string

const (
	taskWaiting  taskState = "waiting"
	taskRunning  taskState = "running"
	taskDone     taskState = "done"
	taskFailed   taskState = "failed"
	taskCanceled taskState = "canceled"
)

type task struct {
	url        string
	state      taskState
	detail     string
	cancel     context.CancelFunc
	downloaded int64
	total      int64
	filesDone  int
	filesTotal int
	lastUpdate time.Time
	createdAt  time.Time
	finishedAt time.Time
}

type Desktop struct {
	app      fyne.App
	window   fyne.Window
	engine   kv.Storage
	stateDir string
	settings Settings
	logger   *zap.Logger

	mu          sync.Mutex
	tasks       []*task
	running     int
	loginCancel context.CancelFunc
	loginInput  chan string

	accountLabel  *widget.Label
	accountButton *widget.Button
	taskList      *widget.List
	taskHint      *widget.Label
	urlEntry      *widget.Entry
	addButton     *widget.Button
}

func Run(ctx context.Context) error {
	d, err := newDesktop()
	if err != nil {
		return err
	}
	defer func() { _ = d.engine.Close(); _ = d.logger.Sync() }()
	d.show(ctx)
	return nil
}

func newDesktop() (*Desktop, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, errors.Wrap(err, "get user config directory")
	}
	root = filepath.Join(root, "telegram-download-bot")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, errors.Wrap(err, "create desktop data directory")
	}
	engine, err := kv.NewWithMap(map[string]string{kv.DriverTypeKey: kv.DriverBolt.String(), "path": filepath.Join(root, "data")})
	if err != nil {
		return nil, errors.Wrap(err, "open desktop storage")
	}
	d := &Desktop{app: app.NewWithID("com.connectingeverycorner.telegramdownloadbot"), engine: engine, stateDir: root, settings: defaultSettings(), logger: logutil.New(zap.InfoLevel, filepath.Join(root, "desktop.log"))}
	d.app.SetIcon(fyne.NewStaticResource("telegram-download-bot.png", iconPNG))
	if err := d.loadSettings(); err != nil {
		_ = engine.Close()
		return nil, err
	}
	if err := d.loadTasks(); err != nil {
		_ = engine.Close()
		return nil, err
	}
	d.applySettings()
	return d, nil
}

func (d *Desktop) show(ctx context.Context) {
	d.window = d.app.NewWindow("Telegram Download Bot Desktop")
	d.window.Resize(fyne.NewSize(820, 580))
	d.window.SetCloseIntercept(func() {
		d.cancelAll()
		d.window.Close()
	})
	d.build(ctx)
	d.window.ShowAndRun()
}

func (d *Desktop) build(ctx context.Context) {
	d.accountLabel = widget.NewLabel(d.tr("checking"))
	d.accountButton = widget.NewButton(d.tr("login"), d.accountAction)
	d.urlEntry = widget.NewEntry()
	d.urlEntry.SetPlaceHolder(d.tr("link_placeholder"))
	d.addButton = widget.NewButton(d.tr("add_download"), d.addTask)
	removeAll := widget.NewButton(d.tr("remove_all"), d.removeAllTasks)
	d.taskHint = widget.NewLabel(d.tr("task_hint"))
	d.taskList = widget.NewList(
		func() int { return d.visibleTaskCount() },
		func() fyne.CanvasObject {
			bar := widget.NewProgressBar()
			bar.Hide()
			return container.NewVBox(widget.NewLabel(d.tr("task")), bar)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			// A progress bar is shown only for active tasks. Keep every virtualized
			// row tall enough for it so it cannot overlap the following task.
			d.taskList.SetItemHeight(id, 72)
			t := d.taskForVisibleID(int(id))
			if t == nil {
				return
			}
			row := o.(*fyne.Container)
			label := row.Objects[0].(*widget.Label)
			bar := row.Objects[1].(*widget.ProgressBar)
			label.SetText(fmt.Sprintf("[%s] %s%s", d.tr("state."+string(t.state)), t.url, detailSuffix(d.taskDetail(t))))
			if t.state == taskRunning && t.total > 0 {
				bar.SetValue(float64(t.downloaded) / float64(t.total))
				bar.Show()
			} else {
				bar.Hide()
			}
		},
	)
	d.taskList.OnSelected = d.taskSelected
	language := widget.NewSelect([]string{d.tr("zh"), d.tr("en")}, func(value string) {
		lang := "zh"
		if value == d.tr("en") {
			lang = "en"
		}
		d.mu.Lock()
		if d.settings.Language == lang {
			d.mu.Unlock()
			return
		}
		d.settings.Language = lang
		d.mu.Unlock()
		_ = d.saveSettings()
		d.rebuild(ctx)
	})
	if d.settings.Language == "en" {
		language.SetSelected(d.tr("en"))
	} else {
		language.SetSelected(d.tr("zh"))
	}

	download := container.NewBorder(
		container.NewVBox(container.NewBorder(nil, nil, nil, container.NewHBox(widget.NewLabel(d.tr("language")), language, d.accountButton), d.accountLabel), container.NewBorder(nil, nil, nil, container.NewHBox(d.addButton, removeAll), d.urlEntry), d.taskHint),
		nil, nil, nil, d.taskList,
	)
	tabs := container.NewAppTabs(
		container.NewTabItem(d.tr("download"), download),
		container.NewTabItem(d.tr("settings"), d.settingsView()),
		container.NewTabItem(d.tr("about"), d.aboutView()),
	)
	d.window.SetContent(tabs)
	d.refreshAccount(ctx)
}

func (d *Desktop) taskDetail(t *task) string {
	if t.state == taskRunning && t.total > 0 {
		percent := float64(t.downloaded) / float64(t.total) * 100
		return fmt.Sprintf("%.1f%% · %s / %s · %d/%d", percent, formatBytes(t.downloaded), formatBytes(t.total), t.filesDone, t.filesTotal)
	}
	return t.detail
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func (d *Desktop) rebuild(ctx context.Context) {
	d.build(ctx)
}

func (d *Desktop) taskSelected(id widget.ListItemID) {
	t := d.taskForVisibleID(int(id))
	if t == nil {
		return
	}
	d.mu.Lock()
	state, link := t.state, t.url
	d.mu.Unlock()
	d.taskList.Unselect(id)
	d.showConfirm(d.tr("remove_task"), d.tr("remove_task_confirm")+"\n"+link, func(ok bool) {
		if !ok {
			return
		}
		d.removeTask(id, state)
	})
}

func (d *Desktop) removeTask(id widget.ListItemID, state taskState) {
	t := d.taskForVisibleID(int(id))
	if t == nil {
		return
	}
	d.mu.Lock()
	if (state == taskWaiting || state == taskRunning) && t.cancel != nil {
		t.cancel()
	}
	for index, candidate := range d.tasks {
		if candidate == t {
			d.tasks = append(d.tasks[:index], d.tasks[index+1:]...)
			break
		}
	}
	d.mu.Unlock()
	_ = d.saveTasks()
	d.refreshTasks()
}

func (d *Desktop) removeAllTasks() {
	d.showConfirm(d.tr("remove_all"), d.tr("remove_all_confirm"), func(ok bool) {
		if !ok {
			return
		}
		d.mu.Lock()
		for _, t := range d.tasks {
			if t.cancel != nil {
				t.cancel()
			}
		}
		d.tasks = nil
		d.mu.Unlock()
		_ = d.saveTasks()
		d.refreshTasks()
	})
}

func (d *Desktop) accountAction() {
	d.mu.Lock()
	loggingIn := d.loginCancel != nil
	d.mu.Unlock()
	if loggingIn {
		d.showError(d.tr("login_in_progress"))
		return
	}
	if d.addButton.Disabled() {
		d.showLogin()
		return
	}
	d.showConfirm(d.tr("logout"), d.tr("logout_confirm"), func(ok bool) {
		if !ok {
			return
		}
		if err := d.logout(); err != nil {
			d.showError(err.Error())
			return
		}
		d.accountLabel.SetText(d.tr("not_logged_in"))
		d.accountButton.SetText(d.tr("login"))
		d.addButton.Disable()
	})
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return " — " + detail
}

func (d *Desktop) settingsView() fyne.CanvasObject {
	appID := widget.NewEntry()
	appID.SetText(fmt.Sprint(d.settings.AppID))
	appHash := widget.NewPasswordEntry()
	appHash.SetText(d.settings.AppHash)
	dir := widget.NewEntry()
	dir.SetText(d.settings.DownloadDir)
	browseDir := widget.NewButton(d.tr("browse_dir"), func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				d.showError(err.Error())
				return
			}
			if uri != nil {
				dir.SetText(uri.Path())
			}
		}, d.window)
	})
	proxy := widget.NewEntry()
	proxy.SetText(d.settings.Proxy)
	proxy.SetPlaceHolder("socks5://127.0.0.1:1080")
	tasks := widget.NewEntry()
	tasks.SetText(fmt.Sprint(d.settings.Tasks))
	threads := widget.NewEntry()
	threads.SetText(fmt.Sprint(d.settings.Threads))
	pool := widget.NewEntry()
	pool.SetText(fmt.Sprint(d.settings.PoolSize))
	save := widget.NewButton(d.tr("settings_save"), func() {
		d.mu.Lock()
		s := Settings{Language: d.settings.Language}
		d.mu.Unlock()
		if _, err := fmt.Sscan(appID.Text, &s.AppID); err != nil || s.AppID < 1 {
			d.showError(d.tr("invalid_api_id"))
			return
		}
		s.AppHash = strings.TrimSpace(appHash.Text)
		if s.AppHash == "" {
			d.showError(d.tr("api_required"))
			return
		}
		if _, err := fmt.Sscan(tasks.Text, &s.Tasks); err != nil || s.Tasks < 1 {
			d.showError(d.tr("invalid_tasks"))
			return
		}
		if _, err := fmt.Sscan(threads.Text, &s.Threads); err != nil || s.Threads < 1 {
			d.showError(d.tr("invalid_threads"))
			return
		}
		if _, err := fmt.Sscan(pool.Text, &s.PoolSize); err != nil || s.PoolSize < 1 {
			d.showError(d.tr("invalid_pool"))
			return
		}
		s.DownloadDir, s.Proxy = strings.TrimSpace(dir.Text), strings.TrimSpace(proxy.Text)
		if s.DownloadDir == "" {
			d.showError(d.tr("choose_dir"))
			return
		}
		d.mu.Lock()
		d.settings = s
		d.mu.Unlock()
		if err := d.saveSettings(); err != nil {
			d.showError(err.Error())
			return
		}
		d.applySettings()
		d.schedule()
		d.taskHint.SetText(d.tr("settings_saved"))
		d.showInformation(d.tr("settings_saved_title"), d.tr("settings_saved"))
	})
	return container.NewVBox(
		widget.NewForm(
			widget.NewFormItem(d.tr("api_id"), appID),
			widget.NewFormItem(d.tr("api_hash"), appHash),
			widget.NewFormItem(d.tr("download_dir"), container.NewBorder(nil, nil, nil, browseDir, dir)),
			widget.NewFormItem(d.tr("proxy"), proxy),
			widget.NewFormItem(d.tr("concurrent_tasks"), tasks),
			widget.NewFormItem(d.tr("download_threads"), threads),
			widget.NewFormItem(d.tr("pool_size"), pool),
		), save,
		d.apiNote(),
		widget.NewLabel(d.tr("settings_note")),
	)
}

func (d *Desktop) apiNote() fyne.CanvasObject {
	return container.NewHBox(
		widget.NewLabel(d.tr("api_note_prefix")),
		widget.NewHyperlink("my.telegram.org", mustURL("https://my.telegram.org/")),
		widget.NewLabel(d.tr("api_note_suffix")),
	)
}

func (d *Desktop) aboutView() fyne.CanvasObject {
	remote := widget.NewLabel(d.tr("remote_version") + d.tr("checking_version"))
	download := widget.NewButton(d.tr("download_latest"), func() {
		if err := d.app.OpenURL(mustURL(releaseURL)); err != nil {
			d.showError(err.Error())
		}
	})
	download.Disable()
	view := container.NewVBox(
		widget.NewLabel("Telegram Download Bot Desktop"),
		widget.NewLabel(d.tr("version")+Version),
		remote,
		download,
		widget.NewLabel(d.tr("about_text")),
		widget.NewHyperlink(d.tr("website"), mustURL("https://ceckit.com/")),
		widget.NewHyperlink("GitHub：telegram-download-bot", mustURL("https://github.com/ConnectingEveryCorner/telegram-download-bot")),
		widget.NewHyperlink("Telegram：@ConnectingEveryCorner", mustURL("https://telegram.me/ConnectingEveryCorner")),
		widget.NewLabel(d.tr("license")),
	)
	go func() {
		tag, err := latestRelease(context.Background())
		fyne.Do(func() {
			if err != nil {
				remote.SetText(d.tr("remote_version") + d.tr("version_check_failed"))
				return
			}
			remote.SetText(d.tr("remote_version") + tag)
			if newerVersion(tag, Version) {
				download.Enable()
			}
		})
	}()
	return view
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func (d *Desktop) addTask() {
	link := strings.TrimSpace(d.urlEntry.Text)
	if !looksLikeTelegramLink(link) {
		d.showError(d.tr("invalid_link"))
		return
	}
	d.mu.Lock()
	d.tasks = append(d.tasks, &task{url: link, state: taskWaiting, createdAt: time.Now()})
	d.mu.Unlock()
	if err := d.saveTasks(); err != nil {
		d.showError(err.Error())
	}
	d.urlEntry.SetText("")
	d.refreshTasks()
	d.schedule()
}

func looksLikeTelegramLink(link string) bool {
	return strings.HasPrefix(link, "https://t.me/") || strings.HasPrefix(link, "http://t.me/") || strings.HasPrefix(link, "https://telegram.me/") || strings.HasPrefix(link, "http://telegram.me/")
}

func (d *Desktop) schedule() {
	d.mu.Lock()
	for d.running < d.settings.Tasks {
		var next *task
		for _, t := range d.tasks {
			if t.state == taskWaiting {
				next = t
				break
			}
		}
		if next == nil {
			break
		}
		next.state, d.running = taskRunning, d.running+1
		ctx, cancel := context.WithCancel(context.Background())
		next.cancel = cancel
		go d.runTask(ctx, next)
	}
	d.mu.Unlock()
	d.refreshTasks()
}

func (d *Desktop) runTask(ctx context.Context, t *task) {
	d.mu.Lock()
	settings := d.settings
	d.mu.Unlock()
	if err := os.MkdirAll(settings.DownloadDir, 0o755); err != nil {
		d.finishTask(t, taskFailed, err.Error())
		return
	}
	kvd, err := d.engine.Open(accountNamespace)
	if err == nil {
		client, clientErr := d.newClient(ctx, kvd, false)
		if clientErr != nil {
			err = clientErr
		} else {
			err = tclientcore.RunWithAuth(logctx.Named(ctx, "desktop-download"), client, func(runCtx context.Context) error {
				return dl.Run(runCtx, client, kvd, dl.Options{Dir: settings.DownloadDir, URLs: []string{t.url}, Template: `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`, Group: true, Continue: true, Restart: true, OnProgress: func(update dl.ProgressUpdate) { d.updateTaskProgress(t, update) }})
			})
		}
	}
	if errors.Is(err, context.Canceled) {
		d.finishTask(t, taskCanceled, d.tr("state.canceled"))
		return
	}
	if err != nil {
		d.finishTask(t, taskFailed, err.Error())
		return
	}
	d.finishTask(t, taskDone, d.tr("saved"))
}

func (d *Desktop) updateTaskProgress(t *task, update dl.ProgressUpdate) {
	d.mu.Lock()
	t.downloaded, t.total = update.Downloaded, update.Total
	t.filesDone, t.filesTotal = update.FilesDone, update.FilesTotal
	if time.Since(t.lastUpdate) < 120*time.Millisecond && update.Downloaded < update.Total {
		d.mu.Unlock()
		return
	}
	t.lastUpdate = time.Now()
	d.mu.Unlock()
	d.refreshTasks()
}

func (d *Desktop) finishTask(t *task, state taskState, detail string) {
	d.mu.Lock()
	t.state, t.detail, t.cancel = state, detail, nil
	t.finishedAt = time.Now()
	d.running--
	d.mu.Unlock()
	_ = d.saveTasks()
	d.refreshTasks()
	d.schedule()
}

func (d *Desktop) cancelAll() {
	d.mu.Lock()
	for _, t := range d.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
	if d.loginCancel != nil {
		d.loginCancel()
	}
	d.mu.Unlock()
}

func (d *Desktop) refreshTasks() {
	fyne.Do(func() {
		if d.taskList != nil {
			d.taskList.Refresh()
		}
	})
}

func (d *Desktop) refreshAccount(ctx context.Context) {
	go func() {
		ok, err := d.authorized(ctx)
		fyne.Do(func() {
			if err != nil {
				d.accountLabel.SetText(d.tr("status_failed") + err.Error())
				return
			}
			if ok {
				d.accountLabel.SetText(d.tr("logged_in"))
				d.accountButton.SetText(d.tr("logout"))
				d.addButton.Enable()
				return
			}
			d.accountLabel.SetText(d.tr("not_logged_in"))
			d.accountButton.SetText(d.tr("login"))
			d.addButton.Disable()
			d.showLogin()
		})
	}()
}

func (d *Desktop) showLogin() {
	phone := widget.NewEntry()
	phone.SetPlaceHolder(d.tr("phone_placeholder"))
	form := dialog.NewForm(d.tr("login_title"), d.tr("login_start"), d.tr("cancel"), []*widget.FormItem{widget.NewFormItem(d.tr("phone"), phone)}, func(ok bool) {
		if !ok {
			return
		}
		if strings.TrimSpace(phone.Text) == "" {
			d.showError(d.tr("phone_required"))
			return
		}
		d.startLogin(strings.TrimSpace(phone.Text))
	}, d.window)
	form.Show()
}

func (d *Desktop) startLogin(phone string) {
	d.mu.Lock()
	if d.loginCancel != nil {
		d.mu.Unlock()
		d.showError(d.tr("login_in_progress"))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.loginCancel, d.loginInput = cancel, make(chan string, 1)
	d.mu.Unlock()
	d.accountLabel.SetText(d.tr("logging_in"))
	go func() {
		err := d.login(ctx, phone)
		d.mu.Lock()
		d.loginCancel, d.loginInput = nil, nil
		d.mu.Unlock()
		fyne.Do(func() {
			if err != nil {
				d.accountLabel.SetText(d.tr("not_logged_in"))
				d.accountButton.SetText(d.tr("login"))
				d.showError(d.tr("login_failed") + err.Error())
				return
			}
			d.accountLabel.SetText(d.tr("logged_in"))
			d.accountButton.SetText(d.tr("logout"))
			d.addButton.Enable()
		})
	}()
}

func (d *Desktop) login(ctx context.Context, phone string) error {
	kvd, err := d.engine.Open(accountNamespace)
	if err != nil {
		return err
	}
	client, err := d.newClient(ctx, kvd, true)
	if err != nil {
		return err
	}
	return client.Run(ctx, func(runCtx context.Context) error {
		flow := auth.NewFlow(&desktopAuth{desktop: d, phone: phone}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(runCtx, flow); err != nil {
			return err
		}
		_, err := client.Self(runCtx)
		return err
	})
}

func (d *Desktop) authorized(ctx context.Context) (bool, error) {
	kvd, err := d.engine.Open(accountNamespace)
	if err != nil {
		return false, err
	}
	client, err := d.newClient(ctx, kvd, false)
	if err != nil {
		return false, err
	}
	var ok bool
	err = client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		ok = status.Authorized
		return err
	})
	return ok, err
}

func (d *Desktop) logout() error {
	kvd, err := d.engine.Open(accountNamespace)
	if err != nil {
		return errors.Wrap(err, "open account storage")
	}
	if err := kvd.Delete(context.Background(), "session"); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return nil
}

func (d *Desktop) newClient(ctx context.Context, kvd storage.Storage, login bool) (*telegram.Client, error) {
	d.mu.Lock()
	settings := d.settings
	d.mu.Unlock()
	if settings.AppID < 1 || strings.TrimSpace(settings.AppHash) == "" {
		return nil, errors.New(d.tr("api_required"))
	}
	return tclientcore.New(ctx, tclientcore.Options{
		AppID:            settings.AppID,
		AppHash:          settings.AppHash,
		Session:          storage.NewSession(kvd, login),
		Proxy:            settings.Proxy,
		ReconnectTimeout: 5 * time.Minute,
	})
}

type desktopAuth struct {
	desktop *Desktop
	phone   string
}

func (a *desktopAuth) Phone(context.Context) (string, error) { return a.phone, nil }
func (a *desktopAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.desktop.awaitLoginInput(ctx, a.desktop.tr("code"), a.desktop.tr("code_prompt"), false)
}
func (a *desktopAuth) Password(ctx context.Context) (string, error) {
	return a.desktop.awaitLoginInput(ctx, a.desktop.tr("password"), a.desktop.tr("password_prompt"), true)
}
func (a *desktopAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New(a.desktop.tr("signup_unsupported"))
}
func (a *desktopAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New(a.desktop.tr("tos_unsupported"))
}

func (d *Desktop) awaitLoginInput(ctx context.Context, title, prompt string, password bool) (string, error) {
	value := make(chan string, 1)
	fyne.Do(func() {
		var entry *widget.Entry
		if password {
			entry = widget.NewPasswordEntry()
		} else {
			entry = widget.NewEntry()
		}
		content := container.NewVBox(
			widget.NewLabel(prompt),
			container.NewGridWrap(fyne.NewSize(440, 48), entry),
		)
		dialog.NewCustomConfirm(title, d.tr("continue"), d.tr("cancel"), content, func(ok bool) {
			if ok {
				value <- strings.TrimSpace(entry.Text)
			} else {
				value <- ""
			}
		}, d.window).Show()
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case input := <-value:
		if input == "" {
			return "", context.Canceled
		}
		return input, nil
	}
}

func (d *Desktop) loadSettings() error {
	b, err := os.ReadFile(filepath.Join(d.stateDir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "read desktop settings")
	}
	if err := json.Unmarshal(b, &d.settings); err != nil {
		return errors.Wrap(err, "parse desktop settings")
	}
	if d.settings.Language == "" {
		d.settings.Language = "zh"
	}
	if d.settings.Tasks < 1 {
		d.settings.Tasks = 2
	}
	if d.settings.Threads < 1 {
		d.settings.Threads = 4
	}
	if d.settings.PoolSize < 1 {
		d.settings.PoolSize = 8
	}
	if d.settings.DownloadDir == "" {
		d.settings.DownloadDir = defaultSettings().DownloadDir
	}
	return nil
}
func (d *Desktop) saveSettings() error {
	b, err := json.MarshalIndent(d.settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.stateDir, "settings.json"), b, 0o600)
}
func (d *Desktop) applySettings() {
	viper.Set(consts.FlagProxy, d.settings.Proxy)
	viper.Set(consts.FlagThreads, d.settings.Threads)
	viper.Set(consts.FlagLimit, d.settings.Tasks)
	viper.Set(consts.FlagPoolSize, d.settings.PoolSize)
}
func (d *Desktop) showConfirm(title, message string, callback func(bool)) {
	dialog.NewCustomConfirm(title, d.tr("confirm"), d.tr("cancel"), widget.NewLabel(message), callback, d.window).Show()
}

func (d *Desktop) showInformation(title, message string) {
	dialog.NewCustom(title, d.tr("ok"), widget.NewLabel(message), d.window).Show()
}

func (d *Desktop) showError(message string) {
	dialog.NewCustom(d.tr("error"), d.tr("ok"), widget.NewLabel(message), d.window).Show()
}
