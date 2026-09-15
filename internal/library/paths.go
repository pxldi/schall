package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PathValidator struct {
	allowed []string
}

func NewPathValidator(allowed []string) (*PathValidator, error) {
	result := &PathValidator{allowed: make([]string, 0, len(allowed))}
	for _, value := range allowed {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		canonical, err := canonicalPath(value, false)
		if err != nil {
			return nil, fmt.Errorf("allowed library root %q: %w", value, err)
		}
		if canonical == string(filepath.Separator) {
			return nil, errors.New("the filesystem root cannot be an allowed library path")
		}
		result.allowed = append(result.allowed, canonical)
	}
	if len(result.allowed) == 0 {
		return nil, errors.New("at least one allowed library root is required")
	}
	return result, nil
}

func (validator *PathValidator) Validate(path string) (string, error) {
	canonical, err := canonicalPath(path, true)
	if err != nil {
		return "", err
	}
	if canonical == string(filepath.Separator) {
		return "", errors.New("the filesystem root cannot be used as a music folder")
	}
	for _, allowed := range validator.allowed {
		relative, err := filepath.Rel(allowed, canonical)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return canonical, nil
		}
	}
	return "", errors.New("path is outside SCHALL_LIBRARY_ALLOWED_ROOTS")
}

func canonicalPath(path string, mustExist bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	path = filepath.Clean(path)
	evaluated, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = evaluated
	} else if mustExist {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if mustExist {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("inspect path: %w", err)
		}
		if !info.IsDir() {
			return "", errors.New("path must be a directory")
		}
	}
	return filepath.Clean(path), nil
}
