// The YAML configuration file: lets a service be configured from a file instead of only from
// environment variables (the other half of this package is the tiered, database-backed platform
// configuration; the two never share state: this one feeds kit/env at process start).
//
// The file is structured the way a person reads it:
//
//	database:
//	  url: postgres://...
//	smtp:
//	  host: mail.example.com
//	  port: 587
//	jwks:
//	  keys_dir: /etc/metap/jwks
//	cors_origins: [https://a.example.com, https://b.example.com]
//
// and every key is the environment variable it replaces, upper-cased with the path joined by "_"
// (database.url is DATABASE_URL, smtp.port is SMTP_PORT, cors_origins is CORS_ORIGINS). Nothing
// that reads configuration changes: kit/env still answers every lookup, it just also looks in the
// file. Order, first match wins: the process environment, then the file, then the reader's own
// default. So a deployment sets the stable values in the file and overrides one per environment
// (or injects a secret) with a variable, with no edit to the file.
//
// Secrets do not belong in the file. Put them in the environment, or in a file outside the
// repository that CONFIG_FILE points at.

package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/metap-org/metap/pkg/kit/env"
)

// FilePathVar names the environment variable that points at the file.
const FilePathVar = "CONFIG_FILE"

var once sync.Once

// LoadFileFromEnv loads the file CONFIG_FILE names, once per process. With the variable unset it does
// nothing (a service with no file runs on the environment alone). A file that is named but cannot
// be read or parsed is an error: running on defaults when someone asked for a file would hide it.
func LoadFileFromEnv() error {
	var err error
	once.Do(func() {
		path := os.Getenv(FilePathVar)
		if path == "" {
			return
		}
		err = LoadFile(path)
	})
	return err
}

// LoadFile reads the YAML file at path and makes its values the fallback source of kit/env.
func LoadFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	values, err := ParseFile(raw)
	if err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	env.SetFallback(func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	})
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	slog.Info("config file loaded", "path", path, "keys", len(names))
	warnAboutSecrets(path, values)
	return nil
}

// secretNameParts mark a setting that holds a credential. A database URL is one when it carries a
// password.
var secretNameParts = []string{"PASSWORD", "SECRET", "TOKEN", "PRIVATE_KEY", "ENCRYPTION_KEYS", "API_KEY", "CREDENTIAL"}

// SecretKeys returns the names in values that look like credentials (names only: a value is never
// returned or logged).
func SecretKeys(values map[string]string) []string {
	var out []string
	for name, value := range values {
		hit := false
		for _, part := range secretNameParts {
			if strings.Contains(name, part) {
				hit = true
				break
			}
		}
		if !hit && strings.HasSuffix(name, "_URL") {
			if u, err := url.Parse(value); err == nil && u.User != nil {
				if _, has := u.User.Password(); has {
					hit = true
				}
			}
		}
		if hit {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// warnAboutSecrets says so when the file holds credentials: they belong in the environment (or in
// a file only the service can read), where a copy of the repository or a shared volume does not
// leak them. A file readable by everyone makes it worse.
func warnAboutSecrets(path string, values map[string]string) {
	keys := SecretKeys(values)
	if len(keys) == 0 {
		return
	}
	worldReadable := false
	if info, err := os.Stat(path); err == nil {
		worldReadable = info.Mode().Perm()&0o004 != 0
	}
	slog.Warn("config file holds credentials: keep them in the environment or a file only the service can read",
		"path", path, "keys", keys, "world_readable", worldReadable)
}

// ParseFile flattens a YAML document into environment-variable names and string values.
func ParseFile(raw []byte) (map[string]string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := flatten("", doc, out); err != nil {
		return nil, err
	}
	return out, nil
}

func flatten(prefix string, node map[string]any, out map[string]string) error {
	for key, value := range node {
		name := strings.ToUpper(key)
		if prefix != "" {
			name = prefix + "_" + name
		}
		switch v := value.(type) {
		case nil:
		case map[string]any:
			if err := flatten(name, v, out); err != nil {
				return err
			}
		case []any:
			items := make([]string, 0, len(v))
			for _, item := range v {
				s, ok := scalar(item)
				if !ok {
					return fmt.Errorf("%s: a list holds only plain values", name)
				}
				items = append(items, s)
			}
			out[name] = strings.Join(items, ",")
		default:
			s, ok := scalar(v)
			if !ok {
				return fmt.Errorf("%s: unsupported value", name)
			}
			out[name] = s
		}
	}
	return nil
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	}
	return "", false
}

// MustLoadFileFromEnv is LoadFileFromEnv for the first line of a main: a file that was asked for and
// cannot be used stops the process with the reason, before anything else reads a setting.
func MustLoadFileFromEnv() {
	if err := LoadFileFromEnv(); err != nil {
		slog.Error("loading the config file", "error", err)
		os.Exit(1)
	}
}
