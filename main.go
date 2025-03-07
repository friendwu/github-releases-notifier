package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/joho/godotenv"

	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// Config of env and args
type Config struct {
	GithubToken     string        `arg:"env:GITHUB_TOKEN"`
	Interval        time.Duration `arg:"env:INTERVAL"`
	LogLevel        string        `arg:"env:LOG_LEVEL"`
	Repositories    []string      `arg:"-r,separate"`
	SlackHook       string        `arg:"env:SLACK_HOOK"`
	IgnoreNonstable bool          `arg:"env:IGNORE_NONSTABLE"`
	ReleasesFile    string        `arg:"env:RELEASES_FILE" default:"releases.json"`
}

// Token returns an oauth2 token or an error.
func (c Config) Token() *oauth2.Token {
	return &oauth2.Token{AccessToken: c.GithubToken}
}

func main() {
	_ = godotenv.Load()

	c := Config{
		Interval: time.Hour,
		LogLevel: "info",
	}
	arg.MustParse(&c)

	logger := log.NewJSONLogger(log.NewSyncWriter(os.Stdout))
	logger = log.With(logger,
		"ts", log.DefaultTimestampUTC,
		"caller", log.Caller(5),
	)

	// level.SetKey("severity")
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		logger = level.NewFilter(logger, level.AllowDebug())
	case "warn":
		logger = level.NewFilter(logger, level.AllowWarn())
	case "error":
		logger = level.NewFilter(logger, level.AllowError())
	default:
		logger = level.NewFilter(logger, level.AllowInfo())
	}

	if len(c.Repositories) == 0 {
		level.Error(logger).Log("msg", "no repositories wo watch")
		os.Exit(1)
	}

	tokenSource := oauth2.StaticTokenSource(c.Token())
	httpClient := oauth2.NewClient(context.Background(), tokenSource)

	checker := &Checker{
		logger:   logger,
		client:   githubv4.NewClient(httpClient),
		filepath: c.ReleasesFile,
	}

	// TODO: releases := make(chan Repository, len(c.Repositories))
	releases := make(chan Repository)
	go checker.Run(c.Interval, c.Repositories, releases)

	slack := SlackSender{Hook: c.SlackHook}

	level.Info(logger).Log("msg", "waiting for new releases")
	for repository := range releases {
		if c.IgnoreNonstable && repository.Release.IsNonstable() {
			level.Debug(logger).Log("msg", "not notifying about non-stable version", "version", repository.Release.Name)
			continue
		}
		if err := slack.Send(repository); err != nil {
			level.Warn(logger).Log(
				"msg", "failed to send release to messenger",
				"err", err,
			)
			continue
		}
		level.Info(logger).Log(
			"msg", "notification sent",
			"repo", repository.Owner+"/"+repository.Name,
			"version", repository.Release.Name,
			"description", repository.Release.Description,
			"url", repository.Release.URL,
		)
	}
}
