package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"

	"github.com/shurcooL/githubv4"
)

// Checker has a githubql client to run queries and also knows about
// the current repositories releases to compare against.
type Checker struct {
	logger   log.Logger
	client   *githubv4.Client
	releases map[string]Repository
	filepath string
}

// LoadReleases loads the saved releases from disk
func (c *Checker) LoadReleases() error {
	data, err := os.ReadFile(c.filepath)
	if err != nil {
		if os.IsNotExist(err) {
			c.releases = make(map[string]Repository)
			return nil
		}
		return fmt.Errorf("failed to read releases file: %w", err)
	}

	if err := json.Unmarshal(data, &c.releases); err != nil {
		return fmt.Errorf("failed to unmarshal releases: %w", err)
	}
	return nil
}

// SaveReleases persists the current releases to disk
func (c *Checker) SaveReleases() error {
	data, err := json.MarshalIndent(c.releases, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal releases: %w", err)
	}

	if err := os.WriteFile(c.filepath, data, 0644); err != nil {
		return fmt.Errorf("failed to write releases file: %w", err)
	}
	return nil
}

// Run the queries and comparisons for the given repositories in a given interval.
func (c *Checker) Run(interval time.Duration, repositories []string, releases chan<- Repository) {
	if c.releases == nil {
		if err := c.LoadReleases(); err != nil {
			level.Error(c.logger).Log("msg", "failed to load releases", "err", err)
			c.releases = make(map[string]Repository)
		}
	}

	for {
		for _, repoName := range repositories {
			s := strings.Split(repoName, "/")
			owner, name := s[0], s[1]

			nextRepo, err := c.query(owner, name)
			if err != nil {
				level.Warn(c.logger).Log(
					"msg", "failed to query the repository's releases",
					"owner", owner,
					"name", name,
					"err", err,
				)
				continue
			}

			level.Info(c.logger).Log(
				"msg", "repository checked",
				"repo", repoName,
				"latest_release", nextRepo.Release.Name,
				"published_at", nextRepo.Release.PublishedAt.Format(time.RFC3339),
			)

			currRepo, ok := c.releases[repoName]

			// We've queried the repository for the first time.
			// Saving the current state to compare with the next iteration.
			if !ok {
				c.releases[repoName] = nextRepo
				if err := c.SaveReleases(); err != nil {
					level.Error(c.logger).Log("msg", "failed to save releases", "err", err)
				}
				continue
			}

			if nextRepo.Release.PublishedAt.After(currRepo.Release.PublishedAt) {
				level.Info(c.logger).Log(
					"msg", "new release found",
					"repo", repoName,
					"previous_version", currRepo.Release.Name,
					"new_version", nextRepo.Release.Name,
				)
				releases <- nextRepo
				c.releases[repoName] = nextRepo
				if err := c.SaveReleases(); err != nil {
					level.Error(c.logger).Log("msg", "failed to save releases", "err", err)
				}
			} else {
				level.Debug(c.logger).Log(
					"msg", "no new release for repository",
					"owner", owner,
					"name", name,
				)
			}
		}
		time.Sleep(interval)
	}
}

// This should be improved in the future to make batch requests for all watched repositories at once
// TODO: https://github.com/shurcooL/githubql/issues/17

func (c *Checker) query(owner, name string) (Repository, error) {
	var query struct {
		Repository struct {
			ID          githubv4.ID
			Name        githubv4.String
			Description githubv4.String
			URL         githubv4.URI

			Releases struct {
				Edges []struct {
					Node struct {
						ID          githubv4.ID
						Name        githubv4.String
						Description githubv4.String
						URL         githubv4.URI
						PublishedAt githubv4.DateTime
					}
				}
			} `graphql:"releases(last: 1, orderBy: { field: CREATED_AT, direction: ASC})"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": githubv4.String(owner),
		"name":  githubv4.String(name),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.client.Query(ctx, &query, variables); err != nil {
		return Repository{}, err
	}

	repositoryID, ok := query.Repository.ID.(string)
	if !ok {
		return Repository{}, fmt.Errorf("can't convert repository id to string: %v", query.Repository.ID)
	}

	if len(query.Repository.Releases.Edges) == 0 {
		return Repository{}, fmt.Errorf("can't find any releases for %s/%s", owner, name)
	}
	latestRelease := query.Repository.Releases.Edges[0].Node

	releaseID, ok := latestRelease.ID.(string)
	if !ok {
		return Repository{}, fmt.Errorf("can't convert release id to string: %v", query.Repository.ID)
	}

	return Repository{
		ID:          repositoryID,
		Name:        string(query.Repository.Name),
		Owner:       owner,
		Description: string(query.Repository.Description),
		URL:         *query.Repository.URL.URL,

		Release: Release{
			ID:          releaseID,
			Name:        string(latestRelease.Name),
			Description: string(latestRelease.Description),
			URL:         *latestRelease.URL.URL,
			PublishedAt: latestRelease.PublishedAt.Time,
		},
	}, nil
}
