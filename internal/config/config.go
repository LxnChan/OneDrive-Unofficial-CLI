package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Config struct {
	ClientID       string   `json:"client_id"`
	Tenant         string   `json:"tenant"`
	Scopes         []string `json:"scopes"`
	UserAgent      string   `json:"user_agent"`
	Proxy          string   `json:"proxy"`
	Token          Token    `json:"token"`
	LoadedFromDisk bool     `json:"-"`
}

func DefaultScopes() []string {
	return []string{"offline_access", "User.Read", "Files.ReadWrite.All"}
}

func DefaultTenant() string {
	return "common"
}

func Dir() (string, error) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return os.Getwd()
	}
	return filepath.Dir(exe), nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{
				Tenant:         DefaultTenant(),
				Scopes:         DefaultScopes(),
				LoadedFromDisk: false,
			}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	cfg.LoadedFromDisk = true
	if cfg.Tenant == "" {
		cfg.Tenant = DefaultTenant()
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = DefaultScopes()
	}
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	cfg.Proxy = strings.TrimSpace(cfg.Proxy)
	return &cfg, nil
}

func Save(cfg *Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	tmp := p + ".tmp"
	perm := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		perm = 0o666
	}
	if err := os.WriteFile(tmp, b, perm); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func ClearToken(cfg *Config) {
	cfg.Token = Token{}
}
