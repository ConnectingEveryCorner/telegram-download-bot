package main

import (
	"context"
	"os"
	"os/signal"
	"strings"

	surveyterm "github.com/AlecAivazis/survey/v2/terminal"
	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/spf13/viper"
	"go.etcd.io/bbolt"

	"github.com/iyear/tdl/app/tgbot"
	"github.com/iyear/tdl/cmd"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	loadConfig()

	if token := strings.TrimSpace(viper.GetString("bot.token")); token != "" {
		if err := tgbot.Run(ctx, tgbot.Options{
			Token:      token,
			Debug:      viper.GetBool("bot.debug"),
			ConfigPath: viper.ConfigFileUsed(),
		}); err != nil && !errors.Is(err, context.Canceled) {
			color.Red("Error: %+v", err)
			os.Exit(1)
		}
		return
	}

	humanizeErrors := map[error]string{
		bbolt.ErrTimeout:        "Current database is used by another process, please terminate it first",
		surveyterm.InterruptErr: "Interrupted",
	}

	if err := cmd.New().ExecuteContext(ctx); err != nil {
		for e, m := range humanizeErrors {
			if errors.Is(err, e) {
				color.Red("%s", m)
				os.Exit(1)
			}
		}

		color.Red("Error: %+v", err)
		os.Exit(1)
	}
}

func loadConfig() {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.SetEnvPrefix("tdl")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	viper.AutomaticEnv()

	viper.SetDefault("bot.debug", false)

	_ = viper.ReadInConfig()
	if viper.ConfigFileUsed() == "" {
		viper.SetConfigFile("config.yaml")
	}
}
