package tools

import (
	"os"
	"strings"
)

const (
	giteaTokenPath          = "/vault/secrets/gitea-token"
	giteaReviewersTokenPath = "/vault/secrets/gitea-reviewers-token"
)

func resolveGiteaToken(token string) string {
	token = strings.TrimSpace(token)
	if token != "" {
		return token
	}
	if data, err := os.ReadFile(giteaTokenPath); err == nil {
		if fileToken := strings.TrimSpace(string(data)); fileToken != "" {
			return fileToken
		}
	}
	return strings.TrimSpace(os.Getenv("GITEA_TOKEN"))
}

func resolveGiteaReviewersToken(token string) string {
	token = strings.TrimSpace(token)
	if token != "" {
		return token
	}
	if data, err := os.ReadFile(giteaReviewersTokenPath); err == nil {
		if fileToken := strings.TrimSpace(string(data)); fileToken != "" {
			return fileToken
		}
	}
	return strings.TrimSpace(os.Getenv("GITEA_REVIEWERS_TOKEN"))
}
