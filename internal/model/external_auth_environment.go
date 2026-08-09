package model

import (
	"errors"
	"regexp"
	"strings"
)

var (
	ErrInvalidExternalAuthEnvironment  = errors.New("invalid external auth environment")
	ErrExternalAuthEnvironmentNotFound = errors.New("external auth environment not found")
	ErrExternalAuthEnvironmentConflict = errors.New("external auth environment already exists")
)

var externalAuthEnvironmentPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

type ExternalAuthEnvironment struct {
	ID          int64  `json:"id"`
	Environment string `json:"environment"`
	AuthzURL    string `json:"authz_url"`
	IsActive    bool   `json:"is_active"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func NormalizeExternalAuthEnvironment(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 64 || !externalAuthEnvironmentPattern.MatchString(value) {
		return "", ErrInvalidExternalAuthEnvironment
	}
	return value, nil
}
