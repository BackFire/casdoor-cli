package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Profile struct {
	Endpoint     string `json:"endpoint"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	Token        string `json:"accessToken,omitempty"`
	Owner        string `json:"owner,omitempty"`
}

type Config struct {
	DefaultProfile string             `json:"defaultProfile"`
	Profiles       map[string]Profile `json:"profiles"`
}

type Options struct {
	Config, Profile, Endpoint, Owner                 string
	File, User, Role, Columns                        string
	Page, PageSize                                   int
	Field, Value, SortField, SortOrder, Organization string
	Timeout                                          time.Duration
	DryRun, ShowSecrets                              bool
}

func loadProfile(o Options) (Profile, error) {
	path := o.Config
	if path == "" {
		path = os.Getenv("CASDOOR_CONFIG")
	}
	explicit := path != ""
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Profile{}, err
		}
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		path = filepath.Join(base, "casdoor", "config.json")
	}
	var cfg Config
	if info, err := os.Stat(path); err == nil {
		if info.Mode().Perm()&0077 != 0 {
			return Profile{}, fmt.Errorf("config must have permissions 0600: %s", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return Profile{}, err
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Profile{}, fmt.Errorf("invalid config JSON: %w", err)
		}
	} else if explicit || !os.IsNotExist(err) {
		return Profile{}, fmt.Errorf("read config: %w", err)
	}
	name := o.Profile
	if name == "" {
		name = os.Getenv("CASDOOR_PROFILE")
	}
	selected := name != ""
	if name == "" {
		name = cfg.DefaultProfile
	}
	p, ok := cfg.Profiles[name]
	if (selected || name != "") && !ok {
		return Profile{}, fmt.Errorf("profile %q not found", name)
	}
	for _, item := range []struct {
		key string
		dst *string
	}{
		{"CASDOOR_ENDPOINT", &p.Endpoint}, {"CASDOOR_CLIENT_ID", &p.ClientID}, {"CASDOOR_CLIENT_SECRET", &p.ClientSecret}, {"CASDOOR_TOKEN", &p.Token}, {"CASDOOR_OWNER", &p.Owner},
	} {
		if value := os.Getenv(item.key); value != "" {
			*item.dst = value
		}
	}
	if o.Endpoint != "" {
		p.Endpoint = o.Endpoint
	}
	if o.Owner != "" {
		p.Owner = o.Owner
	}
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return Profile{}, fmt.Errorf("endpoint must be an HTTPS URL without credentials, query or fragment (HTTP allowed on loopback)")
	}
	p.Endpoint = strings.TrimRight(p.Endpoint, "/")
	if p.Token == "" && (p.ClientID == "" || p.ClientSecret == "") {
		return Profile{}, fmt.Errorf("set clientId/clientSecret or accessToken in a profile or CASDOOR_* environment variables")
	}
	return p, nil
}
