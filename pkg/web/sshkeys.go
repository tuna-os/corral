package web

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// SSH keys in the create wizard can be a literal public key or a GitHub
// username — "gh:torvalds", "github:torvalds", or "@torvalds" — resolved
// against github.com/<user>.keys at create time.

var githubKeysBase = "https://github.com" // test seam

var githubUserRe = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,38})$`)

// resolveSSHKey expands GitHub-username references to the user's public keys
// (newline-joined). Literal keys and empty strings pass through; an
// unresolvable username returns an error rather than silently creating a VM
// without access.
func resolveSSHKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	user := ""
	switch {
	case strings.HasPrefix(s, "gh:"):
		user = strings.TrimPrefix(s, "gh:")
	case strings.HasPrefix(s, "github:"):
		user = strings.TrimPrefix(s, "github:")
	case strings.HasPrefix(s, "@"):
		user = strings.TrimPrefix(s, "@")
	default:
		return s, nil
	}
	user = strings.TrimSpace(user)
	if !githubUserRe.MatchString(user) {
		return "", fmt.Errorf("%q is not a valid GitHub username", user)
	}
	cli := &http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Get(githubKeysBase + "/" + user + ".keys")
	if err != nil {
		return "", fmt.Errorf("fetching GitHub keys for %s: %w", user, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub user %q: keys endpoint returned %d", user, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	keys := strings.TrimSpace(string(body))
	if keys == "" {
		return "", fmt.Errorf("GitHub user %q has no public SSH keys", user)
	}
	return keys, nil
}
