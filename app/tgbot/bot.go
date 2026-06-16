package tgbot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/message"
	messagepeer "github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/iyear/tdl/app/dl"
	"github.com/iyear/tdl/core/logctx"
	cstorage "github.com/iyear/tdl/core/storage"
	tclientcore "github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/core/util/logutil"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/key"
	"github.com/iyear/tdl/pkg/kv"
	"github.com/iyear/tdl/pkg/tclient"
)

const botNamespace = "bot-controller"

const botRestartDelay = 10 * time.Second

type Options struct {
	Token      string
	Debug      bool
	ConfigPath string
}

type Bot struct {
	opts   Options
	kv     kv.Storage
	logger *zap.Logger
	config *configManager
	i18n   *localizer

	mu       sync.Mutex
	raw      *tg.Client
	sender   *message.Sender
	sessions map[int64]*session
}

type session struct {
	namespace string
	peer      tg.InputPeerClass

	mu          sync.Mutex
	loginActive bool
	loginPrompt loginPrompt
	loginInput  chan string
	loginCancel context.CancelFunc
	downloadRun bool
}

type loginPrompt string

const (
	loginPromptNone     loginPrompt = ""
	loginPromptPhone    loginPrompt = "phone"
	loginPromptCode     loginPrompt = "code"
	loginPromptPassword loginPrompt = "password"
)

type botAuth struct {
	bot          *Bot
	chatID       int64
	initialPhone string
}

type mediaKind string

const (
	mediaKindText     mediaKind = "text"
	mediaKindPhoto    mediaKind = "photo"
	mediaKindDocument mediaKind = "document"
)

type originalItem struct {
	MessageID int
	Kind      mediaKind
	Text      string
	Entities  []tg.MessageEntityClass
	MimeType  string
	Attrs     []tg.DocumentAttributeClass
	Spoiler   bool
}

func Run(ctx context.Context, opts Options) error {
	if strings.TrimSpace(opts.Token) == "" {
		return errors.New("empty bot token")
	}
	if strings.TrimSpace(opts.ConfigPath) == "" {
		return errors.New("empty config path")
	}

	appID := viper.GetInt("telegram.app-id")
	appHash := strings.TrimSpace(viper.GetString("telegram.app-hash"))
	if appID == 0 || appHash == "" {
		return errors.New("MTProto bot mode requires telegram.app-id and telegram.app-hash")
	}

	setBotDefaults()

	i18n, err := newLocalizer(viper.GetString("bot.language"))
	if err != nil {
		return err
	}

	level := zap.InfoLevel
	if opts.Debug {
		level = zap.DebugLevel
	}
	logger := logutil.New(level, filepath.Join(consts.LogPath, "bot.log"))
	defer func() { _ = logger.Sync() }()

	engine, err := kv.NewWithMap(map[string]string{
		kv.DriverTypeKey: kv.DriverBolt.String(),
		"path":           filepath.Join(consts.DataDir, "data"),
	})
	if err != nil {
		return errors.Wrap(err, "create kv storage")
	}
	defer func() { _ = engine.Close() }()

	dispatcher := tg.NewUpdateDispatcher()
	b := &Bot{
		opts:     opts,
		kv:       engine,
		logger:   logger,
		config:   newConfigManager(opts.ConfigPath),
		i18n:     i18n,
		sessions: make(map[int64]*session),
	}
	dispatcher.OnNewMessage(b.onNewMessage)
	dispatcher.OnNewChannelMessage(func(context.Context, tg.Entities, *tg.UpdateNewChannelMessage) error {
		return nil
	})

	ctx = logctx.With(ctx, logger)

	botKV, err := engine.Open(botNamespace)
	if err != nil {
		return errors.Wrap(err, "open bot namespace")
	}

	for {
		err := b.runOnce(ctx, appID, appHash, botKV, dispatcher)
		if errors.Is(err, context.Canceled) {
			return err
		}
		b.clearClients()

		fields := []zap.Field{zap.Duration("delay", botRestartDelay)}
		if err != nil {
			fields = append(fields, zap.Error(err))
		}
		logger.Warn("MTProto bot stopped, restarting after delay", fields...)
		if err := sleepContext(ctx, botRestartDelay); err != nil {
			return err
		}
	}
}

func (b *Bot) runOnce(ctx context.Context, appID int, appHash string, botKV cstorage.Storage, dispatcher tg.UpdateDispatcher) (rerr error) {
	defer func() {
		if v := recover(); v != nil {
			rerr = errors.Errorf("bot run panic: %v\n%s", v, debug.Stack())
		}
	}()

	client, err := tclientcore.New(ctx, tclientcore.Options{
		AppID:            appID,
		AppHash:          appHash,
		Session:          cstorage.NewSession(botKV, false),
		Proxy:            viper.GetString(consts.FlagProxy),
		NTP:              viper.GetString(consts.FlagNTP),
		ReconnectTimeout: viper.GetDuration(consts.FlagReconnectTimeout),
		UpdateHandler:    dispatcher,
	})
	if err != nil {
		return errors.Wrap(err, "create bot client")
	}
	defer b.clearClients()

	return client.Run(ctx, func(ctx context.Context) error {
		return b.runAuthorizedBot(ctx, client)
	})
}

func (b *Bot) runAuthorizedBot(ctx context.Context, client *telegram.Client) error {
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return errors.Wrap(err, "check bot auth status")
	}
	if !status.Authorized {
		if _, err := client.Auth().Bot(ctx, b.opts.Token); err != nil {
			return errors.Wrap(err, "bot login")
		}
	}

	self, err := client.Self(ctx)
	if err != nil {
		return errors.Wrap(err, "get bot self")
	}

	raw := tg.NewClient(client)
	b.setRawClient(raw)
	b.setSender(message.NewSender(raw))
	b.logger.Info("MTProto bot started",
		zap.Int64("bot_id", self.ID),
		zap.String("username", self.Username),
	)

	<-ctx.Done()
	return ctx.Err()
}

func setBotDefaults() {
	viper.SetDefault(consts.FlagProxy, viper.GetString("tdl.proxy"))
	viper.SetDefault(consts.FlagThreads, viper.GetInt("tdl.threads"))
	viper.SetDefault(consts.FlagLimit, viper.GetInt("tdl.limit"))
	viper.SetDefault(consts.FlagPoolSize, viper.GetInt("tdl.pool"))
	viper.SetDefault(consts.FlagDelay, viper.GetDuration("tdl.delay"))
	viper.SetDefault(consts.FlagNTP, viper.GetString("tdl.ntp"))
	viper.SetDefault(consts.FlagReconnectTimeout, viper.GetDuration("tdl.reconnect-timeout"))
	viper.SetDefault(consts.FlagDlTemplate, `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`)

	if viper.GetInt(consts.FlagThreads) == 0 {
		viper.SetDefault(consts.FlagThreads, 4)
	}
	if viper.GetInt(consts.FlagLimit) == 0 {
		viper.SetDefault(consts.FlagLimit, 2)
	}
	if viper.GetInt(consts.FlagPoolSize) == 0 {
		viper.SetDefault(consts.FlagPoolSize, 8)
	}
	if viper.GetDuration(consts.FlagReconnectTimeout) == 0 {
		viper.SetDefault(consts.FlagReconnectTimeout, 5*time.Minute)
	}
}

func (b *Bot) onNewMessage(ctx context.Context, entities tg.Entities, upd *tg.UpdateNewMessage) error {
	m, ok := upd.Message.(*tg.Message)
	if !ok || m.Out {
		return nil
	}
	if !isPrivatePeer(m.PeerID) {
		b.logger.Info("Ignore non-private bot message",
			zap.String("peer_kind", peerKind(m.PeerID)),
		)
		return nil
	}

	text := strings.TrimSpace(m.Message)
	if text == "" {
		return nil
	}

	chatID := tutil.GetPeerID(m.PeerID)
	if chatID == 0 {
		return nil
	}

	peer, err := messagepeer.EntitiesFromUpdate(entities).ExtractPeer(m.PeerID)
	if err != nil {
		b.logger.Warn("Extract input peer failed", zap.Error(err))
		return nil
	}

	sess := b.getSession(chatID)
	sess.setPeer(peer)
	b.logger.Info("Received bot message",
		zap.Int64("chat_id", chatID),
		zap.String("text", text),
	)

	go func() {
		defer b.recoverPanic("handle bot message",
			zap.Int64("chat_id", chatID),
		)

		if err := b.handleMessage(ctx, sess, chatID, text); err != nil {
			b.logger.Warn("Handle message failed",
				zap.Int64("chat_id", chatID),
				zap.Error(err),
			)
			_ = b.sendText(ctx, sess, b.tr("handle_failed", err.Error()))
		}
	}()

	return nil
}

func (b *Bot) handleMessage(ctx context.Context, sess *session, chatID int64, text string) error {
	command, arg := parseCommand(text)
	switch command {
	case "/start":
		return b.sendText(ctx, sess, b.tr("start"))
	case "/help":
		return b.sendText(ctx, sess, b.tr("help"))
	case "/myid":
		return b.sendText(ctx, sess, b.tr("my_id", chatID))
	case "/grant":
		return b.handleGrant(ctx, sess, chatID, arg)
	case "/revoke":
		return b.handleRevoke(ctx, sess, chatID, arg)
	case "/users":
		return b.handleUsers(ctx, sess, chatID)
	case "/login":
		if err := b.requireAuthorized(chatID); err != nil {
			return b.sendText(ctx, sess, err.Error())
		}
		return b.startLogin(ctx, sess, chatID, strings.TrimSpace(arg))
	case "/cancel":
		if err := b.requireAuthorized(chatID); err != nil {
			return b.sendText(ctx, sess, err.Error())
		}
		if sess.cancelLogin() {
			return b.sendText(ctx, sess, b.tr("login_cancelled"))
		}
		return b.sendText(ctx, sess, b.tr("login_not_active"))
	case "/logout":
		if err := b.requireAuthorized(chatID); err != nil {
			return b.sendText(ctx, sess, err.Error())
		}
		if err := b.logout(ctx, chatID); err != nil {
			return b.sendText(ctx, sess, b.tr("logout_failed", err.Error()))
		}
		return b.sendText(ctx, sess, b.tr("logout_success"))
	case "/status":
		if err := b.requireAuthorized(chatID); err != nil {
			return b.sendText(ctx, sess, err.Error())
		}
		ok, err := b.isAuthorized(ctx, chatID)
		switch {
		case err != nil:
			return b.sendText(ctx, sess, b.tr("status_failed", err.Error()))
		case ok:
			return b.sendText(ctx, sess, b.tr("status_logged_in"))
		default:
			return b.sendText(ctx, sess, b.tr("status_not_logged_in"))
		}
	}

	if consumed := sess.consumeLoginInput(text); consumed {
		return nil
	}
	if err := b.requireAuthorized(chatID); err != nil {
		return b.sendText(ctx, sess, err.Error())
	}

	if !looksLikeTelegramLink(text) {
		return b.sendText(ctx, sess, b.tr("unsupported_input"))
	}

	if !sess.tryStartDownload() {
		return b.sendText(ctx, sess, b.tr("download_busy"))
	}
	defer sess.finishDownload()

	return b.processLink(ctx, sess, chatID, text)
}

func (b *Bot) handleGrant(ctx context.Context, sess *session, actorChatID int64, arg string) error {
	if err := b.requireAdmin(actorChatID); err != nil {
		return b.sendText(ctx, sess, err.Error())
	}

	target, err := parseChatID(arg)
	if err != nil {
		return b.sendText(ctx, sess, b.tr("grant_usage"))
	}
	if err := b.config.grant(target); err != nil {
		return b.sendText(ctx, sess, b.tr("config_write_failed", err.Error()))
	}
	return b.sendText(ctx, sess, b.tr("granted", target))
}

func (b *Bot) handleRevoke(ctx context.Context, sess *session, actorChatID int64, arg string) error {
	if err := b.requireAdmin(actorChatID); err != nil {
		return b.sendText(ctx, sess, err.Error())
	}

	target, err := parseChatID(arg)
	if err != nil {
		return b.sendText(ctx, sess, b.tr("revoke_usage"))
	}

	snapshot, err := b.config.auth()
	if err != nil {
		return b.sendText(ctx, sess, b.tr("config_read_failed", err.Error()))
	}
	if target == snapshot.AdminChatID {
		return b.sendText(ctx, sess, b.tr("revoke_admin_denied"))
	}

	if err := b.config.revoke(target); err != nil {
		return b.sendText(ctx, sess, b.tr("config_write_failed", err.Error()))
	}
	return b.sendText(ctx, sess, b.tr("revoked", target))
}

func (b *Bot) handleUsers(ctx context.Context, sess *session, actorChatID int64) error {
	if err := b.requireAdmin(actorChatID); err != nil {
		return b.sendText(ctx, sess, err.Error())
	}

	snapshot, err := b.config.auth()
	if err != nil {
		return b.sendText(ctx, sess, b.tr("config_read_failed", err.Error()))
	}

	lines := []string{
		fmt.Sprintf("admin: %d", snapshot.AdminChatID),
	}
	if len(snapshot.AllowedChatIDs) == 0 {
		lines = append(lines, "allowed: (empty)")
	} else {
		lines = append(lines, "allowed:")
		for _, id := range snapshot.AllowedChatIDs {
			lines = append(lines, fmt.Sprintf("- %d", id))
		}
	}

	return b.sendText(ctx, sess, strings.Join(lines, "\n"))
}

func (b *Bot) startLogin(ctx context.Context, sess *session, chatID int64, phone string) error {
	if !sess.beginLogin() {
		return errors.New(b.tr("login_already_active"))
	}

	if err := b.sendText(ctx, sess, b.tr("login_start")); err != nil {
		sess.endLogin()
		return err
	}

	go func() {
		defer b.recoverPanic("login flow",
			zap.Int64("chat_id", chatID),
		)
		defer sess.endLogin()

		loginCtx, cancel := context.WithCancel(ctx)
		sess.setLoginCancel(cancel)
		defer cancel()

		if err := b.loginAccount(loginCtx, chatID, phone); err != nil {
			_ = b.sendText(loginCtx, sess, b.tr("login_failed", err.Error()))
			return
		}
		_ = b.sendText(loginCtx, sess, b.tr("login_success"))
	}()

	return nil
}

func (b *Bot) loginAccount(ctx context.Context, chatID int64, phone string) error {
	kvd, err := b.kv.Open(b.getSession(chatID).namespace)
	if err != nil {
		return errors.Wrap(err, "open namespace")
	}

	client, err := b.newTelegramClient(logctx.Named(ctx, "login"), kvd, true)
	if err != nil {
		return errors.Wrap(err, "create client")
	}

	return client.Run(ctx, func(ctx context.Context) error {
		if err := client.Ping(ctx); err != nil {
			return err
		}

		authFlow := auth.NewFlow(&botAuth{
			bot:          b,
			chatID:       chatID,
			initialPhone: strings.TrimSpace(phone),
		}, auth.SendCodeOptions{})

		if err := client.Auth().IfNecessary(ctx, authFlow); err != nil {
			return err
		}

		_, err := client.Self(ctx)
		return err
	})
}

func (b *Bot) logout(ctx context.Context, chatID int64) error {
	kvd, err := b.kv.Open(b.getSession(chatID).namespace)
	if err != nil {
		return errors.Wrap(err, "open namespace")
	}

	if err := kvd.Delete(ctx, "session"); err != nil {
		return err
	}
	if err := kvd.Delete(ctx, key.App()); err != nil {
		return err
	}
	return nil
}

func (b *Bot) isAuthorized(ctx context.Context, chatID int64) (bool, error) {
	kvd, err := b.kv.Open(b.getSession(chatID).namespace)
	if err != nil {
		return false, errors.Wrap(err, "open namespace")
	}

	client, err := b.newTelegramClient(logctx.Named(ctx, "status"), kvd, false)
	if err != nil {
		return false, errors.Wrap(err, "create client")
	}

	var authorized bool
	if err := client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		authorized = status.Authorized
		return nil
	}); err != nil {
		return false, err
	}

	return authorized, nil
}

func (b *Bot) processLink(ctx context.Context, sess *session, chatID int64, link string) error {
	b.logger.Info("Start processing link",
		zap.Int64("chat_id", chatID),
		zap.String("link", link),
	)

	ok, err := b.isAuthorized(ctx, chatID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New(b.tr("account_not_logged_in"))
	}

	if err := b.sendText(ctx, sess, b.tr("process_start")); err != nil {
		return err
	}

	kvd, err := b.kv.Open(b.getSession(chatID).namespace)
	if err != nil {
		return errors.Wrap(err, "open namespace")
	}

	tmpDir := filepath.Join(consts.DataDir, "bot-downloads", strconv.FormatInt(chatID, 10), time.Now().Format("20060102150405"))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return errors.Wrap(err, "create download dir")
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	client, err := b.newTelegramClient(logctx.Named(ctx, "download"), kvd, false)
	if err != nil {
		return errors.Wrap(err, "create client")
	}

	var original []originalItem
	if err := tclientcore.RunWithAuth(logctx.Named(ctx, "download"), client, func(ctx context.Context) error {
		var err error
		original, err = b.fetchOriginalItems(logctx.Named(ctx, "source"), client, kvd, link)
		if err != nil {
			return errors.Wrap(err, "fetch original message")
		}
		if len(original) == 1 && original[0].Kind == mediaKindText {
			return nil
		}

		return dl.Run(ctx, client, kvd, dl.Options{
			Dir:      tmpDir,
			URLs:     []string{link},
			Template: viper.GetString(consts.FlagDlTemplate),
			Group:    true,
			Continue: true,
			Restart:  true,
		})
	}); err != nil {
		return err
	}

	if len(original) == 1 && original[0].Kind == mediaKindText {
		b.logger.Info("Replay text-only message",
			zap.Int64("chat_id", chatID),
			zap.Int("message_id", original[0].MessageID),
		)
		return b.sendTextWithEntities(ctx, sess, original[0].Text, original[0].Entities)
	}

	files, err := listFiles(tmpDir)
	if err != nil {
		return errors.Wrap(err, "list downloaded files")
	}
	if len(files) == 0 {
		return errors.New(b.tr("no_files"))
	}
	byMessageID, err := filesByMessageID(files)
	if err != nil {
		return errors.Wrap(err, "map downloaded files")
	}

	if len(files) > 1 {
		if err := b.sendText(ctx, sess, b.tr("files_found", len(files))); err != nil {
			return err
		}
	}

	for _, item := range original {
		if len(original) > 1 {
			break
		}
		if item.Kind == mediaKindText {
			if err := b.sendTextWithEntities(ctx, sess, item.Text, item.Entities); err != nil {
				return errors.Wrapf(err, "send text %d", item.MessageID)
			}
			continue
		}

		file, ok := byMessageID[item.MessageID]
		if !ok {
			return errors.New(b.tr("missing_downloaded_file", item.MessageID))
		}
		if err := b.sendMediaWithMeta(ctx, sess, file, item); err != nil {
			return errors.Wrapf(err, "send file %s", filepath.Base(file))
		}
	}

	if len(original) > 1 {
		if err := b.sendAlbumWithMeta(ctx, sess, byMessageID, original); err != nil {
			return errors.Wrap(err, "send album")
		}
	}

	b.logger.Info("Finished processing link",
		zap.Int64("chat_id", chatID),
		zap.String("link", link),
		zap.Int("files", len(files)),
	)

	return nil
}

func listFiles(root string) ([]string, error) {
	var files []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return nil, err
	}

	sort.Strings(files)
	return files, nil
}

func filesByMessageID(paths []string) (map[int]string, error) {
	out := make(map[int]string, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		parts := strings.SplitN(base, "_", 3)
		if len(parts) < 3 {
			return nil, errors.Errorf("unexpected downloaded file name: %s", base)
		}
		msgID, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, errors.Wrapf(err, "parse message id from %s", base)
		}
		out[msgID] = path
	}
	return out, nil
}

func (b *Bot) fetchOriginalItems(ctx context.Context, client *telegram.Client, kvd cstorage.Storage, link string) ([]originalItem, error) {
	raw := tg.NewClient(client)
	manager := peers.Options{Storage: cstorage.NewPeers(kvd)}.Build(raw)

	peer, msgID, err := tutil.ParseMessageLink(ctx, manager, link)
	if err != nil {
		return nil, err
	}
	inputPeer := peer.InputPeer()
	msg, err := tutil.GetSingleMessage(ctx, raw, inputPeer, msgID)
	if err != nil {
		return nil, err
	}

	messages := []*tg.Message{msg}
	if _, ok := msg.GetGroupedID(); ok {
		grouped, err := tutil.GetGroupedMessages(ctx, raw, inputPeer, msg)
		if err == nil && len(grouped) > 0 {
			messages = grouped
		}
	}

	items := make([]originalItem, 0, len(messages))
	for _, m := range messages {
		items = append(items, buildOriginalItem(m))
	}
	return items, nil
}

func buildOriginalItem(m *tg.Message) originalItem {
	item := originalItem{
		MessageID: m.ID,
		Kind:      mediaKindText,
		Text:      m.Message,
		Entities:  cloneMessageEntities(m.Entities),
	}

	md, ok := m.GetMedia()
	if !ok {
		return item
	}

	switch media := md.(type) {
	case *tg.MessageMediaPhoto:
		item.Kind = mediaKindPhoto
		item.Spoiler = media.GetSpoiler()
	case *tg.MessageMediaDocument:
		item.Kind = mediaKindDocument
		doc, ok := media.Document.(*tg.Document)
		if ok {
			item.MimeType = doc.MimeType
			item.Attrs = cloneDocumentAttrs(doc.Attributes)
		}
	default:
		item.Kind = mediaKindDocument
	}

	return item
}

func cloneMessageEntities(in []tg.MessageEntityClass) []tg.MessageEntityClass {
	if len(in) == 0 {
		return nil
	}
	out := make([]tg.MessageEntityClass, len(in))
	copy(out, in)
	return out
}

func cloneDocumentAttrs(in []tg.DocumentAttributeClass) []tg.DocumentAttributeClass {
	if len(in) == 0 {
		return nil
	}
	out := make([]tg.DocumentAttributeClass, len(in))
	copy(out, in)
	return out
}

func (b *Bot) newTelegramClient(ctx context.Context, kvd cstorage.Storage, login bool) (*telegram.Client, error) {
	appID := viper.GetInt("telegram.app-id")
	appHash := strings.TrimSpace(viper.GetString("telegram.app-hash"))
	if appID > 0 && appHash != "" {
		return tclientcore.New(ctx, tclientcore.Options{
			AppID:            appID,
			AppHash:          appHash,
			Session:          cstorage.NewSession(kvd, login),
			Proxy:            viper.GetString(consts.FlagProxy),
			NTP:              viper.GetString(consts.FlagNTP),
			ReconnectTimeout: viper.GetDuration(consts.FlagReconnectTimeout),
			UpdateHandler:    nil,
		})
	}

	if login {
		if err := kvd.Set(ctx, key.App(), []byte(tclient.AppDesktop)); err != nil {
			return nil, errors.Wrap(err, "set app")
		}
	}

	return tclient.New(ctx, tclient.Options{
		KV:               kvd,
		Proxy:            viper.GetString(consts.FlagProxy),
		NTP:              viper.GetString(consts.FlagNTP),
		ReconnectTimeout: viper.GetDuration(consts.FlagReconnectTimeout),
		UpdateHandler:    nil,
	}, login)
}

func (b *Bot) getSession(chatID int64) *session {
	b.mu.Lock()
	defer b.mu.Unlock()

	if s, ok := b.sessions[chatID]; ok {
		return s
	}

	s := &session{
		namespace: fmt.Sprintf("bot-%d", chatID),
	}
	b.sessions[chatID] = s
	return s
}

func (b *Bot) setSender(sender *message.Sender) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sender = sender
}

func (b *Bot) setRawClient(raw *tg.Client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.raw = raw
}

func (b *Bot) clearClients() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.raw = nil
	b.sender = nil
}

func (b *Bot) getSender() (*message.Sender, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sender == nil {
		return nil, errors.New("bot sender is not ready")
	}
	return b.sender, nil
}

func (b *Bot) getRawClient() (*tg.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.raw == nil {
		return nil, errors.New("bot raw client is not ready")
	}
	return b.raw, nil
}

func (b *Bot) recoverPanic(operation string, fields ...zap.Field) {
	if v := recover(); v != nil {
		fields = append(fields,
			zap.String("operation", operation),
			zap.Any("panic", v),
			zap.ByteString("stack", debug.Stack()),
		)
		b.logger.Error("Recovered bot panic", fields...)
	}
}

func (s *session) setPeer(peer tg.InputPeerClass) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peer = peer
}

func (s *session) getPeer() (tg.InputPeerClass, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peer == nil {
		return nil, errors.New("chat peer is not ready")
	}
	return s.peer, nil
}

func (s *session) beginLogin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loginActive {
		return false
	}

	s.loginActive = true
	s.loginPrompt = loginPromptNone
	s.loginInput = make(chan string, 1)
	s.loginCancel = nil
	return true
}

func (s *session) setLoginCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loginCancel = cancel
}

func (s *session) cancelLogin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.loginActive || s.loginCancel == nil {
		return false
	}

	s.loginCancel()
	return true
}

func (s *session) endLogin() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loginActive = false
	s.loginPrompt = loginPromptNone
	s.loginInput = nil
	s.loginCancel = nil
}

func (s *session) waitForPrompt(kind loginPrompt) chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loginPrompt = kind
	return s.loginInput
}

func (s *session) clearPrompt(kind loginPrompt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loginPrompt == kind {
		s.loginPrompt = loginPromptNone
	}
}

func (s *session) consumeLoginInput(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.loginActive || s.loginPrompt == loginPromptNone || s.loginInput == nil {
		return false
	}

	select {
	case s.loginInput <- text:
		return true
	default:
		return false
	}
}

func (s *session) tryStartDownload() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.downloadRun {
		return false
	}
	s.downloadRun = true
	return true
}

func (s *session) finishDownload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloadRun = false
}

func (a *botAuth) Phone(ctx context.Context) (string, error) {
	if a.initialPhone != "" {
		return a.initialPhone, nil
	}

	return a.bot.awaitInput(ctx, a.chatID, loginPromptPhone, a.bot.tr("phone_prompt"))
}

func (a *botAuth) Password(ctx context.Context) (string, error) {
	return a.bot.awaitInput(ctx, a.chatID, loginPromptPassword, a.bot.tr("password_prompt"))
}

func (a *botAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: tos}
}

func (a *botAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New(a.bot.tr("signup_unsupported"))
}

func (a *botAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	_ = a.bot.sendText(ctx, a.bot.getSession(a.chatID), a.bot.tr("code_sent"))
	return a.bot.awaitInput(ctx, a.chatID, loginPromptCode, a.bot.tr("code_waiting"))
}

func (b *Bot) awaitInput(ctx context.Context, chatID int64, kind loginPrompt, prompt string) (string, error) {
	sess := b.getSession(chatID)
	input := sess.waitForPrompt(kind)
	defer sess.clearPrompt(kind)

	if err := b.sendText(ctx, sess, prompt); err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-input:
		return strings.TrimSpace(value), nil
	}
}

func (b *Bot) requireAuthorized(chatID int64) error {
	snapshot, err := b.config.auth()
	if err != nil {
		return errors.Wrap(err, "read auth config")
	}
	if snapshot.AdminChatID == 0 {
		return errors.New(b.tr("admin_not_configured"))
	}
	if chatID == snapshot.AdminChatID {
		return nil
	}
	for _, allowed := range snapshot.AllowedChatIDs {
		if chatID == allowed {
			return nil
		}
	}
	return errors.New(b.tr("unauthorized", chatID, chatID))
}

func (b *Bot) requireAdmin(chatID int64) error {
	snapshot, err := b.config.auth()
	if err != nil {
		return errors.Wrap(err, "read auth config")
	}
	if snapshot.AdminChatID == 0 {
		return errors.New(b.tr("admin_not_configured"))
	}
	if chatID != snapshot.AdminChatID {
		return errors.New(b.tr("admin_required"))
	}
	return nil
}

func parseChatID(arg string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid chat id")
	}
	return id, nil
}

func parseCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}

	parts := strings.SplitN(text, " ", 2)
	cmd := strings.SplitN(parts[0], "@", 2)[0]
	if len(parts) == 1 {
		return cmd, ""
	}
	return cmd, parts[1]
}

func looksLikeTelegramLink(text string) bool {
	return strings.HasPrefix(text, "https://t.me/") || strings.HasPrefix(text, "http://t.me/") ||
		strings.HasPrefix(text, "https://telegram.me/") || strings.HasPrefix(text, "http://telegram.me/")
}

func isPrivatePeer(peer tg.PeerClass) bool {
	_, ok := peer.(*tg.PeerUser)
	return ok
}

func peerKind(peer tg.PeerClass) string {
	switch peer.(type) {
	case *tg.PeerUser:
		return "user"
	case *tg.PeerChat:
		return "chat"
	case *tg.PeerChannel:
		return "channel"
	default:
		return "unknown"
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (b *Bot) sendText(ctx context.Context, sess *session, text string) error {
	sender, err := b.getSender()
	if err != nil {
		return err
	}
	peer, err := sess.getPeer()
	if err != nil {
		return err
	}
	_, err = sender.To(peer).NoWebpage().Text(ctx, text)
	return err
}

func (b *Bot) sendTextWithEntities(ctx context.Context, sess *session, text string, entities []tg.MessageEntityClass) error {
	peer, err := sess.getPeer()
	if err != nil {
		return err
	}
	if len(entities) == 0 {
		return b.sendText(ctx, sess, text)
	}

	raw, err := b.getRawClient()
	if err != nil {
		return err
	}
	randomID, err := crypto.RandInt64(crypto.DefaultRand())
	if err != nil {
		return err
	}
	req := &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  text,
		RandomID: randomID,
	}
	req.SetNoWebpage(true)
	req.SetEntities(cloneMessageEntities(entities))
	_, err = raw.MessagesSendMessage(ctx, req)
	return err
}

func (b *Bot) sendMediaWithMeta(ctx context.Context, sess *session, path string, item originalItem) error {
	peer, err := sess.getPeer()
	if err != nil {
		return err
	}
	raw, err := b.getRawClient()
	if err != nil {
		return err
	}

	file, err := uploader.NewUploader(raw).WithPartSize(512*1024).FromPath(ctx, path)
	if err != nil {
		return errors.Wrap(err, "upload")
	}
	randomID, err := crypto.RandInt64(crypto.DefaultRand())
	if err != nil {
		return err
	}

	var media tg.InputMediaClass
	switch item.Kind {
	case mediaKindPhoto:
		photo := &tg.InputMediaUploadedPhoto{File: file}
		if item.Spoiler {
			photo.SetSpoiler(true)
		}
		media = photo
	default:
		doc := &tg.InputMediaUploadedDocument{
			File:       file,
			MimeType:   item.MimeType,
			Attributes: cloneDocumentAttrs(item.Attrs),
		}
		if item.Spoiler {
			doc.SetSpoiler(true)
		}
		media = doc
	}

	req := &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    media,
		Message:  item.Text,
		RandomID: randomID,
	}
	if len(item.Entities) > 0 {
		req.SetEntities(cloneMessageEntities(item.Entities))
	}
	_, err = raw.MessagesSendMedia(ctx, req)
	return err
}

func (b *Bot) sendAlbumWithMeta(ctx context.Context, sess *session, files map[int]string, items []originalItem) error {
	peer, err := sess.getPeer()
	if err != nil {
		return err
	}
	raw, err := b.getRawClient()
	if err != nil {
		return err
	}

	upl := uploader.NewUploader(raw).WithPartSize(512 * 1024)
	album := make([]tg.InputSingleMedia, 0, len(items))
	for _, item := range items {
		path, ok := files[item.MessageID]
		if !ok {
			return errors.New(b.tr("missing_downloaded_file", item.MessageID))
		}

		media, err := b.uploadAlbumMedia(ctx, peer, upl, path, item)
		if err != nil {
			return errors.Wrapf(err, "prepare album media %d", item.MessageID)
		}
		randomID, err := crypto.RandInt64(crypto.DefaultRand())
		if err != nil {
			return err
		}

		single := tg.InputSingleMedia{
			Media:    media,
			RandomID: randomID,
			Message:  item.Text,
		}
		if len(item.Entities) > 0 {
			single.SetEntities(cloneMessageEntities(item.Entities))
		}
		album = append(album, single)
	}

	req := &tg.MessagesSendMultiMediaRequest{
		Peer:       peer,
		MultiMedia: album,
	}
	_, err = raw.MessagesSendMultiMedia(ctx, req)
	return err
}

func (b *Bot) uploadAlbumMedia(ctx context.Context, peer tg.InputPeerClass, upl *uploader.Uploader, path string, item originalItem) (tg.InputMediaClass, error) {
	file, err := upl.FromPath(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "upload file")
	}

	var uploadMedia tg.InputMediaClass
	switch item.Kind {
	case mediaKindPhoto:
		photo := &tg.InputMediaUploadedPhoto{File: file}
		if item.Spoiler {
			photo.SetSpoiler(true)
		}
		uploadMedia = photo
	default:
		doc := &tg.InputMediaUploadedDocument{
			File:       file,
			MimeType:   item.MimeType,
			Attributes: cloneDocumentAttrs(item.Attrs),
		}
		if item.Spoiler {
			doc.SetSpoiler(true)
		}
		uploadMedia = doc
	}

	raw, err := b.getRawClient()
	if err != nil {
		return nil, err
	}
	uploaded, err := raw.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{
		Peer:  peer,
		Media: uploadMedia,
	})
	if err != nil {
		return nil, errors.Wrap(err, "upload media")
	}

	return convertMessageMediaToInput(uploaded)
}

func convertMessageMediaToInput(m tg.MessageMediaClass) (tg.InputMediaClass, error) {
	switch v := m.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := v.Photo.AsNotEmpty()
		if !ok {
			return nil, errors.Errorf("unexpected photo type %T", v.Photo)
		}
		return &tg.InputMediaPhoto{
			ID:         photo.AsInput(),
			TTLSeconds: v.TTLSeconds,
			Spoiler:    v.GetSpoiler(),
		}, nil
	case *tg.MessageMediaDocument:
		document, ok := v.Document.AsNotEmpty()
		if !ok {
			return nil, errors.Errorf("unexpected document type %T", v.Document)
		}
		return &tg.InputMediaDocument{
			ID:         document.AsInput(),
			TTLSeconds: v.TTLSeconds,
			Spoiler:    v.GetSpoiler(),
		}, nil
	default:
		return nil, errors.Errorf("unsupported album media type %T", v)
	}
}
