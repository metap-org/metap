// Package configfile lets a service be configured from a YAML file instead of only from
// environment variables.
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
package configfile

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/metap-org/metap/pkg/kit/env"
)

// PathVar names the environment variable that points at the file.
const PathVar = "CONFIG_FILE"

var once sync.Once

// LoadFromEnv loads the file CONFIG_FILE names, once per process. With the variable unset it does
// nothing (a service with no file runs on the environment alone). A file that is named but cannot
// be read or parsed is an error: running on defaults when someone asked for a file would hide it.
func LoadFromEnv() error {
	var err error
	once.Do(func() {
		path := os.Getenv(PathVar)
		if path == "" {
			return
		}
		err = Load(path)
	})
	return err
}

// Load reads the YAML file at path and makes its values the fallback source of kit/env.
func Load(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	values, err := Parse(raw)
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
	return nil
}

// Parse flattens a YAML document into environment-variable names and string values.
func Parse(raw []byte) (map[string]string, error) {
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

// MustLoadFromEnv is LoadFromEnv for the first line of a main: a file that was asked for and
// cannot be used stops the process with the reason, before anything else reads a setting.
func MustLoadFromEnv() {
	if err := LoadFromEnv(); err != nil {
		slog.Error("loading the config file", "error", err)
		os.Exit(1)
	}
}
