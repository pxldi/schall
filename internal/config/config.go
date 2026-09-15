package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pxldi/schall/internal/navidrome"
)

// The two things SCHALL_AUTH can say. Open is the default and is what Schall
// did before it could authenticate anybody.
const (
	AuthOpen  = "open"
	AuthProxy = "proxy"
)

type Config struct {
	HTTPAddress          string
	DatabaseURL          string
	LogLevel             string
	MusicBrainzBaseURL   string
	MusicBrainzUserAgent string
	LibraryAllowedRoots  []string
	DownloadInboxPath    string
	ImportLibraryPath    string
	// UploadStagingPath is where a browser upload lands before anything is
	// decided about it. It is never a library root and never inside one: a scan
	// that caught a half-written upload would index a truncated file as music
	// the library holds, so startup checks the paths rather than trusting this.
	UploadStagingPath string
	// FpcalcPath is the Chromaprint binary that turns audio into a fingerprint.
	// It is configurable because the image may carry it anywhere, and empty
	// means "find fpcalc on PATH".
	FpcalcPath string
	// YtdlpPath is the yt-dlp binary that searches YouTube and cuts an excerpt
	// from an upload, for the anchors of docs/decisions/0029. Empty means no
	// want is anchored from YouTube.
	YtdlpPath string
	// FFmpegPath is what turns a fetched copy into something a browser will play,
	// so a review item can be listened to before it is decided about. Configurable
	// on the same terms as fpcalc; empty means "find ffmpeg on PATH", and not
	// finding it costs the preview and nothing else.
	FFmpegPath string
	// PreviewCachePath is where those transcodes are kept between requests. It is
	// scratch by nature — deleting it costs a re-encode — so it defaults under the
	// system temporary directory rather than asking to be configured.
	PreviewCachePath string
	// NavidromePathMap says how the library's path in Schall differs from the
	// same library's path in Navidrome, as a comma-separated list of
	// local=remote pairs such as "/music=/music-schall". The two read the same
	// directory through their own mounts, and playlist sync compares paths
	// across that boundary, so a deployment whose mounts disagree needs this or
	// sync pairs nothing at all. Empty is the identity mapping, which is right
	// wherever the mounts already agree.
	NavidromePathMap navidrome.PathMap
	// AuthMode says who Schall believes. "open" is what Schall did before it
	// could authenticate anybody: every request is served and the actor is the
	// machine. "proxy" serves a request only when a reverse proxy vouched for
	// the identity headers or the request carried an app token.
	AuthMode string
	// AuthProxyKey is the value the reverse proxy puts in X-Schall-Proxy-Key.
	// The X-authentik-* headers are ordinary request headers that anything
	// reaching the pod can write, so they are believed only beside this.
	AuthProxyKey string
	// AuthTrustedProxies is the weaker way to say the same thing: the networks
	// a request may arrive from for its identity headers to be believed. It is
	// checked against the TCP peer address, so every pod on a listed network is
	// a trusted proxy.
	AuthTrustedProxies []netip.Prefix
	ShutdownTimeout    time.Duration
	// DatabaseStartupTimeout bounds how long startup waits for a database that
	// is still accepting no connections, so a database started alongside
	// Schall does not present as a crash loop.
	DatabaseStartupTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddress: envOrDefault("SCHALL_HTTP_ADDRESS", ":8080"),
		DatabaseURL: strings.TrimSpace(os.Getenv("SCHALL_DATABASE_URL")),
		LogLevel:    strings.ToLower(envOrDefault("SCHALL_LOG_LEVEL", "info")),
		MusicBrainzBaseURL: envOrDefault(
			"SCHALL_MUSICBRAINZ_BASE_URL",
			"https://musicbrainz.org/ws/2",
		),
		MusicBrainzUserAgent: envOrDefault(
			"SCHALL_MUSICBRAINZ_USER_AGENT",
			"Schall/0.1.0 (https://github.com/pxldi/schall)",
		),
		LibraryAllowedRoots: splitPaths(envOrDefault("SCHALL_LIBRARY_ALLOWED_ROOTS", "/music")),
		DownloadInboxPath:   strings.TrimSpace(os.Getenv("SCHALL_DOWNLOAD_INBOX_PATH")),
		ImportLibraryPath:   strings.TrimSpace(os.Getenv("SCHALL_IMPORT_LIBRARY_PATH")),
		UploadStagingPath:   strings.TrimSpace(os.Getenv("SCHALL_UPLOAD_STAGING_PATH")),
		FpcalcPath:          strings.TrimSpace(os.Getenv("SCHALL_FPCALC_PATH")),
		YtdlpPath:           strings.TrimSpace(os.Getenv("SCHALL_YTDLP_PATH")),
		FFmpegPath:          strings.TrimSpace(os.Getenv("SCHALL_FFMPEG_PATH")),
		PreviewCachePath: envOrDefault(
			"SCHALL_PREVIEW_CACHE_PATH", filepath.Join(os.TempDir(), "schall-previews"),
		),
		AuthMode:        strings.ToLower(envOrDefault("SCHALL_AUTH", AuthOpen)),
		AuthProxyKey:    strings.TrimSpace(os.Getenv("SCHALL_AUTH_PROXY_KEY")),
		ShutdownTimeout: 10 * time.Second,
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("SCHALL_DATABASE_URL is required")
	}

	startupTimeout, err := envDuration("SCHALL_DATABASE_STARTUP_TIMEOUT", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.DatabaseStartupTimeout = startupTimeout

	// Refused at startup rather than at the first sync: a half-written mapping
	// is indistinguishable, from inside a pass, from a player that reports
	// display paths, and both present as a sync that recognises nothing.
	pathMap, err := navidrome.ParsePathMap(os.Getenv("SCHALL_NAVIDROME_PATH_MAP"))
	if err != nil {
		return Config{}, fmt.Errorf("SCHALL_NAVIDROME_PATH_MAP: %w", err)
	}
	cfg.NavidromePathMap = pathMap

	// The managed library is where both acquisition routes put what they take
	// in, so each of them needs it named — and nothing else uses it, so on its
	// own it configures nothing. The two routes are otherwise independent: an
	// installation that only ever uploads must not be made to name a
	// completed-download folder it does not have.
	if cfg.DownloadInboxPath != "" && cfg.ImportLibraryPath == "" {
		return Config{}, fmt.Errorf("SCHALL_DOWNLOAD_INBOX_PATH and SCHALL_IMPORT_LIBRARY_PATH must be set together")
	}
	// Staging is where an upload waits; the managed library is where it goes
	// afterwards. Accepting one without the other would be a folder that fills
	// up with music nothing can ever take in, so it is refused here rather than
	// discovered by the first upload.
	if cfg.UploadStagingPath != "" && cfg.ImportLibraryPath == "" {
		return Config{}, fmt.Errorf("SCHALL_UPLOAD_STAGING_PATH requires SCHALL_IMPORT_LIBRARY_PATH")
	}
	if cfg.ImportLibraryPath != "" && cfg.DownloadInboxPath == "" && cfg.UploadStagingPath == "" {
		return Config{}, fmt.Errorf(
			"SCHALL_IMPORT_LIBRARY_PATH is only used by SCHALL_DOWNLOAD_INBOX_PATH and SCHALL_UPLOAD_STAGING_PATH; set one of them")
	}
	for name, path := range map[string]string{
		"SCHALL_DOWNLOAD_INBOX_PATH": cfg.DownloadInboxPath,
		"SCHALL_IMPORT_LIBRARY_PATH": cfg.ImportLibraryPath,
		"SCHALL_UPLOAD_STAGING_PATH": cfg.UploadStagingPath,
	} {
		if path != "" && !filepath.IsAbs(path) {
			return Config{}, fmt.Errorf("%s must be absolute", name)
		}
	}

	proxies, err := parsePrefixes(os.Getenv("SCHALL_AUTH_TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, fmt.Errorf("SCHALL_AUTH_TRUSTED_PROXIES: %w", err)
	}
	cfg.AuthTrustedProxies = proxies

	switch cfg.AuthMode {
	case AuthOpen, AuthProxy:
	default:
		return Config{}, fmt.Errorf("SCHALL_AUTH must be %s or %s", AuthOpen, AuthProxy)
	}
	// Neither way of vouching for the identity headers leaves a mode where the
	// browser can never authenticate and only a token can, and a token cannot
	// mint the first token. Refused here rather than found by the person who
	// has just locked themselves out of Settings.
	if cfg.AuthMode == AuthProxy && cfg.AuthProxyKey == "" && len(cfg.AuthTrustedProxies) == 0 {
		return Config{}, fmt.Errorf(
			"SCHALL_AUTH=%s needs SCHALL_AUTH_PROXY_KEY or SCHALL_AUTH_TRUSTED_PROXIES", AuthProxy)
	}

	return cfg, nil
}

// parsePrefixes reads a comma-separated list of CIDR blocks such as
// "10.42.0.0/16,fd00::/8". An unparsable entry is an error rather than a
// silently dropped one: a trusted network nobody noticed was ignored is a
// deployment that refuses every browser request.
func parsePrefixes(value string) ([]netip.Prefix, error) {
	parts := strings.Split(value, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR block such as 10.42.0.0/16", part)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func splitPaths(value string) []string {
	parts := strings.Split(value, string(os.PathListSeparator))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

// envDuration reads a Go duration such as "30s". An unparsable value is an
// error rather than a silent fallback, because a timeout that quietly differs
// from what an operator configured is worse than a refused startup.
func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 60s: %w", name, err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must not be negative", name)
	}
	return parsed, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
