package main

import (
	"context"
	"log"
	"os"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	githubactions "github.com/sethvargo/go-githubactions"
	renderaction "github.com/tobiash/flux-helm-preview/pkg/action"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	zerologr.NameFieldName = "logger"
	zerologr.NameSeparator = "/"

	zl := zerolog.New(os.Stderr)
	zl = zl.With().Caller().Timestamp().Logger().Output(zerolog.ConsoleWriter{Out: os.Stderr})
	logger := zerologr.New(&zl)

	ctx := logr.NewContext(context.Background(), logger)
	ghaction := githubactions.New()
	cfg, err := renderaction.NewFromInputs(ghaction)
	if err != nil {
		log.Fatal(err)
	}
	act, err := renderaction.NewAction(ctx, cfg, ghaction)

	if err != nil {
		log.Fatal(err)
	}

	if err := act.Run(); err != nil {
		log.Fatal(err)
	}
}