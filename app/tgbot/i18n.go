package tgbot

import (
	"embed"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"gopkg.in/yaml.v3"
)

//go:embed locales/*.yaml
var localeFS embed.FS

type localizer struct {
	lang     string
	messages map[string]string
}

func newLocalizer(lang string) (*localizer, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return nil, errors.New("bot.language is required, supported values: en, zh")
	}
	if lang != "en" && lang != "zh" {
		return nil, errors.Errorf("unsupported bot.language %q, supported values: en, zh", lang)
	}

	data, err := localeFS.ReadFile("locales/" + lang + ".yaml")
	if err != nil {
		return nil, errors.Wrap(err, "read locale")
	}

	messages := map[string]string{}
	if err := yaml.Unmarshal(data, &messages); err != nil {
		return nil, errors.Wrap(err, "parse locale")
	}
	return &localizer{lang: lang, messages: messages}, nil
}

func (l *localizer) T(key string, args ...any) string {
	if l == nil {
		return key
	}
	msg := l.messages[key]
	if msg == "" {
		msg = key
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

func (b *Bot) tr(key string, args ...any) string {
	return b.i18n.T(key, args...)
}
