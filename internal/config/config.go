// Package config loads Reelix configuration from the environment.
//
// Every setting has a REELIX_-prefixed environment variable and a default that
// is correct for the container image. Unparseable values are a startup error,
// never a silent fallback to the default.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Defaults applied when the corresponding environment variable is unset or
// empty. They describe the containerised deployment, which is canonical.
const (
	DefaultHTTPAddr        = ":8080"
	DefaultLogLevel        = "info"
	DefaultLogFormat       = "json"
	DefaultConfigDir       = "/config"
	DefaultCacheDir        = "/cache"
	DefaultShutdownTimeout = 15 * time.Second

	DefaultDBHost    = "postgres"
	DefaultDBPort    = 5432
	DefaultDBName    = "reelix"
	DefaultDBUser    = "reelix"
	DefaultDBSSLMode = "disable"

	// The jellyfin-ffmpeg7 package installs here. Reelix shells out to these
	// binaries; it never links them.
	DefaultFFprobePath  = "/usr/lib/jellyfin-ffmpeg/ffprobe"
	DefaultFFmpegPath   = "/usr/lib/jellyfin-ffmpeg/ffmpeg"
	DefaultProbeTimeout = 2 * time.Minute

	// TMDB is the movie identity provider. The base URL is configurable so a
	// test can point at an httptest server without the suite reaching the
	// real internet.
	DefaultTMDBBaseURL = "https://api.themoviedb.org/3"

	// DefaultTMDBImageBaseURL is where image BYTES come from. A different host
	// from the API, and a different kind of request: no key, no JSON, and a
	// response measured in megabytes rather than kilobytes.
	//
	// TMDB publishes this base through its /configuration endpoint. It is
	// defaulted here with an override rather than discovered, so that starting
	// the server costs no provider request and a change of CDN is one
	// environment variable rather than a release.
	DefaultTMDBImageBaseURL = "https://image.tmdb.org/t/p"
	// One provider call. Identity makes one search per unidentified item, so
	// this bounds a single request, not the pass.
	DefaultMetadataTimeout = 15 * time.Second

	// DefaultImageTimeout bounds one image download.
	//
	// Longer than a metadata request because it is a different shape of
	// transfer: a 1280-wide backdrop is a couple of megabytes where a JSON
	// response is a couple of kilobytes, and a timeout tuned for the second
	// would abandon the first on any slow link.
	DefaultImageTimeout = 60 * time.Second

	// DefaultMetadataRegion selects which country's certification becomes
	// OfficialRating. US is the default because it is right for most users of
	// an English-language media server, not because it is correct in general —
	// hence the setting.
	DefaultMetadataRegion = "US"
)

// Config is the fully resolved configuration for one server process.
type Config struct {
	HTTP     HTTP
	Log      Log
	Paths    Paths
	Database Database
	Media    Media
	Metadata Metadata
}

// Metadata configures the external metadata provider.
//
// TMDBAPIKey is REQUIRED, and that is deliberate. A key that is missing is a
// configuration mistake, and the ffprobe precedent says a configuration
// mistake belongs at startup naming the variable, not minutes later when
// somebody triggers an identify pass and it fails on its first item.
//
// Note the consequence: an instance that only ever browses and plays local
// files still needs a key to boot. That is the cost of finding out early.
//
// Reachability is NOT checked at startup, and the asymmetry with ffprobe is
// intentional. ffprobe is a local binary, so its absence is permanent and
// knowable. TMDB is a remote service that can be down for reasons that have
// nothing to do with this deployment, and refusing to start a media server
// because a metadata API is unreachable would turn someone else's outage into
// ours — the same reasoning that keeps identification out of the scan.
type Metadata struct {
	TMDBAPIKey string
	// TMDBBaseURL exists so tests can redirect the provider. Deployments
	// should leave it alone.
	TMDBBaseURL string
	// TMDBImageBaseURL is the CDN base for image bytes; see the default.
	TMDBImageBaseURL string
	// RequestTimeout bounds one provider HTTP request.
	RequestTimeout time.Duration
	// ImageTimeout bounds one image download, which is a much larger transfer
	// than a metadata request; see DefaultImageTimeout.
	ImageTimeout time.Duration

	// Region is the ISO 3166-1 country whose film certification becomes
	// OfficialRating, e.g. "US", "GB", "DE".
	//
	// A region with no certification for a film yields an EMPTY rating. It
	// does NOT fall back to another region: showing a US rating to someone who
	// configured GB is a wrong answer that looks like a right one, and nothing
	// downstream could tell the difference. See officialRating.
	Region string
}

// Media configures the external media tools.
//
// The constitution requires both binary paths to be configurable with sane
// defaults for the container image. FFmpegPath is unused until transcoding
// arrives; it is loaded and validated now so that the configuration surface
// does not change again for it.
type Media struct {
	FFprobePath string
	FFmpegPath  string
	// ProbeTimeout bounds a single ffprobe invocation. A probe that hangs on
	// one file must not stall an entire library scan.
	ProbeTimeout time.Duration
}

// HTTP configures the public listener.
type HTTP struct {
	// Addr is a Go listen address, e.g. ":8080" or "127.0.0.1:8080".
	Addr string
	// ShutdownTimeout bounds how long in-flight requests may take to drain
	// after a termination signal.
	ShutdownTimeout time.Duration
}

// Log configures the structured logger.
type Log struct {
	// Level is one of debug, info, warn, error.
	Level string
	// Format is one of json, text.
	Format string
}

// Paths are the persistent locations owned by the server. They live outside
// the application container in the canonical deployment.
type Paths struct {
	ConfigDir string
	CacheDir  string
}

// Database describes how to reach PostgreSQL.
//
// Step 1 loads and validates these values but does not open a connection; the
// driver and repository layer arrive with the schema in Step 2.
type Database struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string
}

var (
	validLogLevels  = []string{"debug", "info", "warn", "error"}
	validLogFormats = []string{"json", "text"}
	validSSLModes   = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}
)

// Load reads configuration from the process environment.
//
// It returns every validation problem it finds rather than only the first, so
// a misconfigured deployment can be fixed in one pass instead of one restart
// per mistake.
func Load() (Config, error) {
	var errs []string
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	cfg := Config{
		HTTP: HTTP{
			Addr:            lookupString("REELIX_HTTP_ADDR", DefaultHTTPAddr),
			ShutdownTimeout: DefaultShutdownTimeout,
		},
		Log: Log{
			Level:  strings.ToLower(lookupString("REELIX_LOG_LEVEL", DefaultLogLevel)),
			Format: strings.ToLower(lookupString("REELIX_LOG_FORMAT", DefaultLogFormat)),
		},
		Paths: Paths{
			ConfigDir: lookupString("REELIX_CONFIG_DIR", DefaultConfigDir),
			CacheDir:  lookupString("REELIX_CACHE_DIR", DefaultCacheDir),
		},
		Database: Database{
			Host:     lookupString("REELIX_DB_HOST", DefaultDBHost),
			Port:     DefaultDBPort,
			Name:     lookupString("REELIX_DB_NAME", DefaultDBName),
			User:     lookupString("REELIX_DB_USER", DefaultDBUser),
			Password: lookupString("REELIX_DB_PASSWORD", ""),
			SSLMode:  strings.ToLower(lookupString("REELIX_DB_SSLMODE", DefaultDBSSLMode)),
		},
		Media: Media{
			FFprobePath:  lookupString("REELIX_FFPROBE_PATH", DefaultFFprobePath),
			FFmpegPath:   lookupString("REELIX_FFMPEG_PATH", DefaultFFmpegPath),
			ProbeTimeout: DefaultProbeTimeout,
		},
		Metadata: Metadata{
			TMDBAPIKey:       lookupString("REELIX_TMDB_API_KEY", ""),
			TMDBBaseURL:      lookupString("REELIX_TMDB_BASE_URL", DefaultTMDBBaseURL),
			TMDBImageBaseURL: lookupString("REELIX_TMDB_IMAGE_BASE_URL", DefaultTMDBImageBaseURL),
			RequestTimeout:   DefaultMetadataTimeout,
			ImageTimeout:     DefaultImageTimeout,
			Region:           strings.ToUpper(lookupString("REELIX_METADATA_REGION", DefaultMetadataRegion)),
		},
	}

	if raw, ok := lookup("REELIX_METADATA_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			fail("REELIX_METADATA_TIMEOUT: %q is not a duration (want e.g. 15s, 1m)", raw)
		case d <= 0:
			fail("REELIX_METADATA_TIMEOUT: must be positive, got %q", raw)
		default:
			cfg.Metadata.RequestTimeout = d
		}
	}

	if raw, ok := lookup("REELIX_IMAGE_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			fail("REELIX_IMAGE_TIMEOUT: %q is not a duration (want e.g. 60s, 2m)", raw)
		case d <= 0:
			fail("REELIX_IMAGE_TIMEOUT: must be positive, got %q", raw)
		default:
			cfg.Metadata.ImageTimeout = d
		}
	}

	if raw, ok := lookup("REELIX_PROBE_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			fail("REELIX_PROBE_TIMEOUT: %q is not a duration (want e.g. 2m, 90s)", raw)
		case d <= 0:
			fail("REELIX_PROBE_TIMEOUT: must be positive, got %q", raw)
		default:
			cfg.Media.ProbeTimeout = d
		}
	}

	if raw, ok := lookup("REELIX_SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			fail("REELIX_SHUTDOWN_TIMEOUT: %q is not a duration (want e.g. 15s, 1m)", raw)
		case d <= 0:
			fail("REELIX_SHUTDOWN_TIMEOUT: must be positive, got %q", raw)
		default:
			cfg.HTTP.ShutdownTimeout = d
		}
	}

	if raw, ok := lookup("REELIX_DB_PORT"); ok {
		port, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			fail("REELIX_DB_PORT: %q is not a number", raw)
		case port < 1 || port > 65535:
			fail("REELIX_DB_PORT: must be between 1 and 65535, got %d", port)
		default:
			cfg.Database.Port = port
		}
	}

	if cfg.HTTP.Addr == "" {
		fail("REELIX_HTTP_ADDR: must not be empty")
	}
	if !slices.Contains(validLogLevels, cfg.Log.Level) {
		fail("REELIX_LOG_LEVEL: %q is not one of %s", cfg.Log.Level, strings.Join(validLogLevels, ", "))
	}
	if !slices.Contains(validLogFormats, cfg.Log.Format) {
		fail("REELIX_LOG_FORMAT: %q is not one of %s", cfg.Log.Format, strings.Join(validLogFormats, ", "))
	}
	if cfg.Paths.ConfigDir == "" {
		fail("REELIX_CONFIG_DIR: must not be empty")
	}
	if cfg.Paths.CacheDir == "" {
		fail("REELIX_CACHE_DIR: must not be empty")
	}
	if cfg.Database.Host == "" {
		fail("REELIX_DB_HOST: must not be empty")
	}
	if cfg.Database.Name == "" {
		fail("REELIX_DB_NAME: must not be empty")
	}
	if cfg.Database.User == "" {
		fail("REELIX_DB_USER: must not be empty")
	}
	if cfg.Database.Password == "" {
		fail("REELIX_DB_PASSWORD: must be set")
	}
	if !slices.Contains(validSSLModes, cfg.Database.SSLMode) {
		fail("REELIX_DB_SSLMODE: %q is not one of %s", cfg.Database.SSLMode, strings.Join(validSSLModes, ", "))
	}
	if cfg.Media.FFprobePath == "" {
		fail("REELIX_FFPROBE_PATH: must not be empty")
	}
	if cfg.Media.FFmpegPath == "" {
		fail("REELIX_FFMPEG_PATH: must not be empty")
	}
	if cfg.Metadata.TMDBAPIKey == "" {
		fail("REELIX_TMDB_API_KEY: must be set (get one at https://www.themoviedb.org/settings/api)")
	}
	if cfg.Metadata.TMDBBaseURL == "" {
		fail("REELIX_TMDB_BASE_URL: must not be empty")
	}
	if len(cfg.Metadata.Region) != 2 {
		fail("REELIX_METADATA_REGION: %q is not a two-letter ISO 3166-1 country code (e.g. US, GB, DE)",
			cfg.Metadata.Region)
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return cfg, nil
}

// DSN renders the PostgreSQL connection URL.
//
// The result contains the database password. It must never be logged; use
// Database.Redacted for anything that is written to a log or an API response.
func (d Database) DSN() string {
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(d.User, d.Password),
		Host:     net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		Path:     "/" + d.Name,
		RawQuery: url.Values{"sslmode": []string{d.SSLMode}}.Encode(),
	}
	return u.String()
}

// Redacted describes the database target without exposing the password. It is
// safe to log.
func (d Database) Redacted() string {
	return fmt.Sprintf("postgres://%s@%s/%s?sslmode=%s",
		d.User, net.JoinHostPort(d.Host, strconv.Itoa(d.Port)), d.Name, d.SSLMode)
}

func lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func lookupString(key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}
