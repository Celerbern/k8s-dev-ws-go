package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

// Config is the CLI's on-disk configuration.
type Config struct {
	ServerURL string `json:"serverUrl"`
	Token     string `json:"token"`
	Username  string `json:"username"`
	SSHHost   string `json:"sshHost,omitempty"`
	SSHPort   int    `json:"sshPort,omitempty"`
}

// ConfigPath returns the path of the CLI config file.
func ConfigPath() string {
	if v := os.Getenv("DEVWS_CONFIG"); v != "" {
		return v
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "devws", "config.json")
}

// LoadConfig reads the config, failing with a helpful message when not
// logged in.
func LoadConfig() (*Config, error) {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, fmt.Errorf("not logged in — run `devws login <server-url>` first")
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("corrupt config %s: %w", ConfigPath(), err)
	}
	if cfg.ServerURL == "" || cfg.Token == "" {
		return nil, fmt.Errorf("not logged in — run `devws login <server-url>` first")
	}
	return &cfg, nil
}

// Save writes the config to disk.
func (c *Config) Save() error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// RemoveConfig deletes the config file.
func RemoveConfig() error {
	err := os.Remove(ConfigPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ------------------------------------------------------------------ http --

type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.Status, e.Body)
}

func apiDo[Resp any](cfg *Config, method, path string, body any) (*Resp, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, cfg.ServerURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var errResp struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &errResp)
		msg := errResp.Error
		if msg == "" {
			msg = string(raw)
		}
		return nil, &httpError{Status: resp.StatusCode, Body: msg}
	}
	var out Resp
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}
	}
	return &out, nil
}

func apiGet[Resp any](cfg *Config, path string) (*Resp, error) {
	return apiDo[Resp](cfg, http.MethodGet, path, nil)
}

func apiPost[Resp any](cfg *Config, path string, body any) (*Resp, error) {
	return apiDo[Resp](cfg, http.MethodPost, path, body)
}

func apiPatch[Resp any](cfg *Config, path string, body any) (*Resp, error) {
	return apiDo[Resp](cfg, http.MethodPatch, path, body)
}

func apiDelete[Resp any](cfg *Config, path string) (*Resp, error) {
	return apiDo[Resp](cfg, http.MethodDelete, path, nil)
}

// apiStream opens a GET whose body is streamed (e.g. workspace logs). The
// caller must close the returned ReadCloser. The stream is aborted when ctx
// is cancelled.
func apiStream(ctx context.Context, cfg *Config, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.ServerURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Accept", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var errResp struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &errResp)
		msg := errResp.Error
		if msg == "" {
			msg = string(raw)
		}
		return nil, &httpError{Status: resp.StatusCode, Body: msg}
	}
	return resp.Body, nil
}

// ----------------------------------------------------------------- types --

type userView struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	AuthMethod  string `json:"authMethod"`
	Disabled    bool   `json:"disabled"`
	Namespace   string `json:"namespace"`
	CreatedAt   string `json:"createdAt"`
	Quotas      struct {
		MaxWorkspaces int    `json:"maxWorkspaces"`
		CPU           string `json:"cpu"`
		Memory        string `json:"memory"`
		Storage       string `json:"storage"`
	} `json:"quotas"`
	SSHPublicKeys []struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Fingerprint string `json:"fingerprint"`
		CreatedAt   string `json:"createdAt"`
	} `json:"sshPublicKeys"`
	WorkspaceCount int `json:"workspaceCount"`
}

type workspaceView struct {
	Name      string   `json:"name"`
	User      string   `json:"user"`
	Namespace string   `json:"namespace"`
	Class     string   `json:"class"`
	Image     string   `json:"image"`
	Storage   string   `json:"storage"`
	GitRepos  []string `json:"gitRepos"`
	Paused    bool     `json:"paused"`
	Phase     string   `json:"phase"`
	Message   string   `json:"message"`
	PodName   string   `json:"podName"`
	SSHHost   string   `json:"sshHost"`
	CreatedAt string   `json:"createdAt"`
}

type eventView struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Object    string `json:"object"`
	Message   string `json:"message"`
	Count     int32  `json:"count"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

type workspaceImageView struct {
	Name        string `json:"name"`
	Image       string `json:"image"`
	Description string `json:"description"`
	Deprecated  bool   `json:"deprecated"`
	UsedBy      int    `json:"usedBy,omitempty"`
}

type platformConfig struct {
	SSHHost        string `json:"sshHost"`
	SSHPort        int    `json:"sshPort"`
	DefaultClass   string `json:"defaultClass"`
	WorkspaceImage string `json:"workspaceImage"`
	Classes        map[string]struct {
		CPU     string `json:"cpu"`
		Memory  string `json:"memory"`
		Storage string `json:"storage"`
	} `json:"classes"`
}

// homeDir returns the user's home directory across platforms.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	switch runtime.GOOS {
	case "windows":
		return os.Getenv("USERPROFILE")
	}
	return os.Getenv("HOME")
}
