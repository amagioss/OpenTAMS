// Package tamsctl holds the reusable client, config, and token logic behind
// the tamsctl operator CLI (see cmd/tamsctl).
package tamsctl

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// EnvToken and EnvEndpoint override the resolved context's token/endpoint.
const (
	EnvToken    = "TAMSCTL_TOKEN" //nolint:gosec // env var name, not a credential.
	EnvEndpoint = "TAMSCTL_ENDPOINT"
)

// Context is a named endpoint + bearer token pair, kubectl-style.
type Context struct {
	Endpoint string `yaml:"endpoint"`
	Token    string `yaml:"token,omitempty"`
}

// Config is the on-disk ~/.tamsctl/config document plus the path it was
// loaded from (so Save writes back to the same place).
type Config struct {
	CurrentContext string             `yaml:"current-context,omitempty"`
	Contexts       map[string]Context `yaml:"contexts,omitempty"`

	path string `yaml:"-"`
}

// Resolved is the effective endpoint/token for a single invocation after
// applying context selection and flag/env overrides.
type Resolved struct {
	Name     string
	Endpoint string
	Token    string
}

// DefaultConfigPath returns ~/.tamsctl/config.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".tamsctl", "config"), nil
}

// Load reads the config at path. A missing file yields an empty config bound
// to that path (so a subsequent Save creates it).
func Load(path string) (*Config, error) {
	cfg := &Config{path: path}
	data, err := os.ReadFile(path) //nolint:gosec // path is the user's own config location.
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.path = path
	return cfg, nil
}

// Save writes the config back to its path with 0600 perms, creating the
// parent directory 0700 if needed.
func (c *Config) Save() error {
	if c.path == "" {
		return fmt.Errorf("config has no path to save to")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(c.path, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", c.path, err)
	}
	return nil
}

// SetContext creates or updates a named context.
func (c *Config) SetContext(name, endpoint, token string) {
	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	existing := c.Contexts[name]
	existing.Endpoint = endpoint
	if token != "" {
		existing.Token = token
	}
	c.Contexts[name] = existing
}

// SetToken updates only the token of an existing context.
func (c *Config) SetToken(name, token string) error {
	ctx, ok := c.Contexts[name]
	if !ok {
		return fmt.Errorf("unknown context %q", name)
	}
	ctx.Token = token
	c.Contexts[name] = ctx
	return nil
}

// UseContext sets current-context, rejecting unknown names.
func (c *Config) UseContext(name string) error {
	if _, ok := c.Contexts[name]; !ok {
		return fmt.Errorf("unknown context %q", name)
	}
	c.CurrentContext = name
	return nil
}

// Resolve computes the effective endpoint/token. Precedence, highest first:
// explicit flag, environment variable (TAMSCTL_*), context value. The context
// is contextOverride if set, else current-context.
func (c *Config) Resolve(contextOverride, endpointFlag, tokenFlag string) (Resolved, error) {
	name := contextOverride
	if name == "" {
		name = c.CurrentContext
	}

	var ctx Context
	if name != "" {
		var ok bool
		ctx, ok = c.Contexts[name]
		if !ok {
			return Resolved{}, fmt.Errorf("unknown context %q", name)
		}
	}

	endpoint := firstNonEmpty(endpointFlag, os.Getenv(EnvEndpoint), ctx.Endpoint)
	token := firstNonEmpty(tokenFlag, os.Getenv(EnvToken), ctx.Token)

	if endpoint == "" {
		return Resolved{}, fmt.Errorf("no endpoint configured: set a context with 'tamsctl config set-context', pass --endpoint, or set %s", EnvEndpoint)
	}
	return Resolved{Name: name, Endpoint: endpoint, Token: token}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
