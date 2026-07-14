package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/go-faster/errors"
)

const releaseURL = "https://github.com/ConnectingEveryCorner/telegram-download-bot/releases/latest"

type githubRelease struct {
	TagName string `json:"tag_name"`
}

func latestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/ConnectingEveryCorner/telegram-download-bot/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.Errorf("GitHub API returned %s", resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return "", errors.New("release has no tag")
	}
	return release.TagName, nil
}

func newerVersion(remote, local string) bool {
	if local == "" || local == "dev" {
		return true
	}
	r, err := semver.NewVersion(remote)
	if err != nil {
		return false
	}
	l, err := semver.NewVersion(local)
	return err == nil && r.GreaterThan(l)
}
