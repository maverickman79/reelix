package config

import (
	"strings"
	"testing"
	"time"
)

// setEnv applies a set of variables for one test, clearing everything else in
// the REELIX_ namespace so tests cannot leak into each other.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range []string{
		"REELIX_HTTP_ADDR", "REELIX_LOG_LEVEL", "REELIX_LOG_FORMAT",
		"REELIX_CONFIG_DIR", "REELIX_CACHE_DIR", "REELIX_SHUTDOWN_TIMEOUT",
		"REELIX_DB_HOST", "REELIX_DB_PORT", "REELIX_DB_NAME",
		"REELIX_DB_USER", "REELIX_DB_PASSWORD", "REELIX_DB_SSLMODE",
	} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// A password is the only mandatory setting; everything else has a default.
func minimalEnv() map[string]string {
	return map[string]string{"REELIX_DB_PASSWORD": "s3cret"}
}

func TestLoadDefaults(t *testing.T) {
	setEnv(t, minimalEnv())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.HTTP.Addr != DefaultHTTPAddr {
		t.Errorf("HTTP.Addr = %q, want %q", cfg.HTTP.Addr, DefaultHTTPAddr)
	}
	if cfg.HTTP.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("HTTP.ShutdownTimeout = %v, want %v", cfg.HTTP.ShutdownTimeout, DefaultShutdownTimeout)
	}
	if cfg.Log.Level != DefaultLogLevel {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, DefaultLogLevel)
	}
	if cfg.Log.Format != DefaultLogFormat {
		t.Errorf("Log.Format = %q, want %q", cfg.Log.Format, DefaultLogFormat)
	}
	if cfg.Paths.ConfigDir != DefaultConfigDir {
		t.Errorf("Paths.ConfigDir = %q, want %q", cfg.Paths.ConfigDir, DefaultConfigDir)
	}
	if cfg.Paths.CacheDir != DefaultCacheDir {
		t.Errorf("Paths.CacheDir = %q, want %q", cfg.Paths.CacheDir, DefaultCacheDir)
	}
	if cfg.Database.Host != DefaultDBHost || cfg.Database.Port != DefaultDBPort {
		t.Errorf("Database host:port = %s:%d, want %s:%d",
			cfg.Database.Host, cfg.Database.Port, DefaultDBHost, DefaultDBPort)
	}
}

func TestLoadOverrides(t *testing.T) {
	setEnv(t, map[string]string{
		"REELIX_HTTP_ADDR":        "127.0.0.1:9000",
		"REELIX_LOG_LEVEL":        "debug",
		"REELIX_LOG_FORMAT":       "text",
		"REELIX_CONFIG_DIR":       "/var/lib/reelix/config",
		"REELIX_CACHE_DIR":        "/var/lib/reelix/cache",
		"REELIX_SHUTDOWN_TIMEOUT": "45s",
		"REELIX_DB_HOST":          "db.internal",
		"REELIX_DB_PORT":          "6543",
		"REELIX_DB_NAME":          "reelix_prod",
		"REELIX_DB_USER":          "reelix_app",
		"REELIX_DB_PASSWORD":      "hunter2",
		"REELIX_DB_SSLMODE":       "require",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.HTTP.Addr != "127.0.0.1:9000" {
		t.Errorf("HTTP.Addr = %q, want 127.0.0.1:9000", cfg.HTTP.Addr)
	}
	if cfg.HTTP.ShutdownTimeout != 45*time.Second {
		t.Errorf("HTTP.ShutdownTimeout = %v, want 45s", cfg.HTTP.ShutdownTimeout)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "text" {
		t.Errorf("Log = %+v, want level=debug format=text", cfg.Log)
	}
	if cfg.Database.Port != 6543 {
		t.Errorf("Database.Port = %d, want 6543", cfg.Database.Port)
	}
	if cfg.Database.SSLMode != "require" {
		t.Errorf("Database.SSLMode = %q, want require", cfg.Database.SSLMode)
	}
}

// Values are trimmed and case-normalised: compose files routinely introduce
// stray whitespace, and "INFO" should not be a startup failure.
func TestLoadNormalisesValues(t *testing.T) {
	env := minimalEnv()
	env["REELIX_LOG_LEVEL"] = "  DEBUG  "
	env["REELIX_LOG_FORMAT"] = "TEXT"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("Log.Format = %q, want text", cfg.Log.Format)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantSub string
	}{
		{"log level", "REELIX_LOG_LEVEL", "verbose", "REELIX_LOG_LEVEL"},
		{"log format", "REELIX_LOG_FORMAT", "xml", "REELIX_LOG_FORMAT"},
		{"db port not a number", "REELIX_DB_PORT", "abc", "REELIX_DB_PORT"},
		{"db port out of range", "REELIX_DB_PORT", "70000", "REELIX_DB_PORT"},
		{"ssl mode", "REELIX_DB_SSLMODE", "maybe", "REELIX_DB_SSLMODE"},
		{"shutdown timeout", "REELIX_SHUTDOWN_TIMEOUT", "soon", "REELIX_SHUTDOWN_TIMEOUT"},
		{"negative shutdown timeout", "REELIX_SHUTDOWN_TIMEOUT", "-5s", "REELIX_SHUTDOWN_TIMEOUT"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := minimalEnv()
			env[tc.key] = tc.value
			setEnv(t, env)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with %s=%q returned no error", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// An unset password must fail loudly rather than silently attempting a
// passwordless connection later.
func TestLoadRequiresPassword(t *testing.T) {
	setEnv(t, map[string]string{})

	if _, err := Load(); err == nil {
		t.Fatal("Load() without REELIX_DB_PASSWORD returned no error")
	} else if !strings.Contains(err.Error(), "REELIX_DB_PASSWORD") {
		t.Errorf("error %q does not mention REELIX_DB_PASSWORD", err)
	}
}

// All problems are reported together so a broken deployment is fixable in one
// pass rather than one restart per mistake.
func TestLoadReportsAllErrors(t *testing.T) {
	setEnv(t, map[string]string{
		"REELIX_LOG_LEVEL":  "verbose",
		"REELIX_LOG_FORMAT": "xml",
	})

	_, err := Load()
	if err == nil {
		t.Fatal("Load() returned no error")
	}
	for _, want := range []string{"REELIX_LOG_LEVEL", "REELIX_LOG_FORMAT", "REELIX_DB_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestDatabaseDSN(t *testing.T) {
	db := Database{
		Host: "postgres", Port: 5432, Name: "reelix",
		User: "reelix", Password: "p@ss word/1", SSLMode: "disable",
	}

	dsn := db.DSN()
	if !strings.HasPrefix(dsn, "postgres://") {
		t.Errorf("DSN() = %q, want a postgres:// URL", dsn)
	}
	// A password containing URL-significant characters must survive encoding.
	if strings.Contains(dsn, "p@ss word/1") {
		t.Errorf("DSN() = %q, password is not URL-encoded", dsn)
	}
	if !strings.Contains(dsn, "sslmode=disable") {
		t.Errorf("DSN() = %q, want sslmode=disable", dsn)
	}
}

// Redacted is what gets logged, so it must never contain the password.
func TestDatabaseRedactedOmitsPassword(t *testing.T) {
	db := Database{
		Host: "postgres", Port: 5432, Name: "reelix",
		User: "reelix", Password: "sup3rs3cret", SSLMode: "disable",
	}

	got := db.Redacted()
	if strings.Contains(got, "sup3rs3cret") {
		t.Errorf("Redacted() = %q, leaks the password", got)
	}
	if !strings.Contains(got, "postgres:5432") || !strings.Contains(got, "reelix") {
		t.Errorf("Redacted() = %q, want the host and database name", got)
	}
}
