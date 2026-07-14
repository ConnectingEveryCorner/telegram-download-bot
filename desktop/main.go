package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/iyear/tdl/app/desktop"
)

// This is the standalone GUI entrypoint used by Fyne packaging. It intentionally
// does not load the Bot configuration or require a "gui" command-line argument.
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := desktop.Run(ctx); err != nil {
		panic(err)
	}
}
