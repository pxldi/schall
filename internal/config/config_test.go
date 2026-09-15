package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "SCHALL_DATABASE_URL") {
		t.Fatalf("Load() error = %v, want missing database URL error", err)
	}
}

func TestLoadUsesDefaults(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_HTTP_ADDRESS", "")
	t.Setenv("SCHALL_LOG_LEVEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddress != ":8080" {
		t.Errorf("HTTPAddress = %q, want :8080", cfg.HTTPAddress)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.MusicBrainzBaseURL != "https://musicbrainz.org/ws/2" {
		t.Errorf("MusicBrainzBaseURL = %q", cfg.MusicBrainzBaseURL)
	}
	if !strings.HasPrefix(cfg.MusicBrainzUserAgent, "Schall/") {
		t.Errorf("MusicBrainzUserAgent = %q", cfg.MusicBrainzUserAgent)
	}
}

func TestLoadDatabaseStartupTimeout(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")

	t.Setenv("SCHALL_DATABASE_STARTUP_TIMEOUT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseStartupTimeout != 60*time.Second {
		t.Errorf("default timeout = %s, want 1m0s", cfg.DatabaseStartupTimeout)
	}

	t.Setenv("SCHALL_DATABASE_STARTUP_TIMEOUT", "5s")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseStartupTimeout != 5*time.Second {
		t.Errorf("configured timeout = %s, want 5s", cfg.DatabaseStartupTimeout)
	}

	t.Setenv("SCHALL_DATABASE_STARTUP_TIMEOUT", "soon")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an unparsable duration")
	}
}

func TestLoadLibraryAllowedRoots(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_LIBRARY_ALLOWED_ROOTS", "/music:/archive")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.LibraryAllowedRoots) != 2 || cfg.LibraryAllowedRoots[1] != "/archive" {
		t.Fatalf("roots = %#v", cfg.LibraryAllowedRoots)
	}
}

func TestLoadDownloadImportPaths(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_DOWNLOAD_INBOX_PATH", " /downloads ")
	t.Setenv("SCHALL_IMPORT_LIBRARY_PATH", " /music ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DownloadInboxPath != "/downloads" || cfg.ImportLibraryPath != "/music" {
		t.Fatalf("paths = %q / %q", cfg.DownloadInboxPath, cfg.ImportLibraryPath)
	}
}

func TestLoadRequiresBothAbsoluteDownloadImportPaths(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_DOWNLOAD_INBOX_PATH", "/downloads")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted only one import path")
	}
	t.Setenv("SCHALL_IMPORT_LIBRARY_PATH", "music")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a relative import path")
	}
}

func TestLoadUploadStagingPath(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_IMPORT_LIBRARY_PATH", "/music")
	t.Setenv("SCHALL_DOWNLOAD_INBOX_PATH", "/downloads")
	t.Setenv("SCHALL_UPLOAD_STAGING_PATH", " /staging ")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UploadStagingPath != "/staging" {
		t.Fatalf("staging path = %q, want /staging", cfg.UploadStagingPath)
	}
}

// Uploading needs somewhere for an upload to go afterwards. Accepting staging
// on its own would be a folder that fills up with music nothing can take in.
func TestLoadRequiresAManagedLibraryToUploadInto(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_UPLOAD_STAGING_PATH", "/staging")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "SCHALL_IMPORT_LIBRARY_PATH") {
		t.Fatalf("Load() error = %v, want the managed library required", err)
	}
}

func TestLoadRequiresAnAbsoluteUploadStagingPath(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_IMPORT_LIBRARY_PATH", "/music")
	t.Setenv("SCHALL_DOWNLOAD_INBOX_PATH", "/downloads")
	t.Setenv("SCHALL_UPLOAD_STAGING_PATH", "staging")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a relative staging path")
	}
}

// Uploading is one of the two acquisition routes and does not go through a
// download source, so it must not require one to be configured.
func TestLoadAcceptsUploadingWithoutADownloadInbox(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_IMPORT_LIBRARY_PATH", "/music")
	t.Setenv("SCHALL_UPLOAD_STAGING_PATH", "/staging")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UploadStagingPath != "/staging" || cfg.DownloadInboxPath != "" {
		t.Fatalf("cfg = %q / %q", cfg.UploadStagingPath, cfg.DownloadInboxPath)
	}
}

// The managed library is used by downloading and uploading and by nothing else,
// so naming it on its own configures nothing at all.
func TestLoadRefusesAManagedLibraryNothingWouldUse(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_IMPORT_LIBRARY_PATH", "/music")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a managed library nothing would put anything in")
	}
}

// Schall and Navidrome read the same directory through their own mounts, so a
// deployment where the two disagree says so once, here.
func TestLoadReadsTheNavidromePathMap(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_NAVIDROME_PATH_MAP", "/music=/music-schall")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.NavidromePathMap.ToRemote("/music/track.flac"); got != "/music-schall/track.flac" {
		t.Fatalf("ToRemote() = %q, want the configured mapping", got)
	}
}

// An unset map is the identity, which is right wherever the mounts agree.
func TestLoadWithoutAPathMapLeavesPathsAlone(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.NavidromePathMap.ToRemote("/music/track.flac"); got != "/music/track.flac" {
		t.Fatalf("ToRemote() = %q, want the path untouched", got)
	}
}

// Refused at startup rather than at the first sync: a mapping that is quietly
// not applied looks exactly like a player reporting display paths.
func TestLoadRefusesAHalfWrittenPathMap(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_NAVIDROME_PATH_MAP", "/music")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a path mapping with one half missing")
	}
}

func TestLoadRefusesARelativePathMap(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_NAVIDROME_PATH_MAP", "music=/music-schall")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a relative path mapping")
	}
}

func TestLoadDefaultsToOpenAuth(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthMode != AuthOpen {
		t.Errorf("AuthMode = %q, want %q", cfg.AuthMode, AuthOpen)
	}
}

// A mode nobody recognises would otherwise be read as open, which is the one
// answer an operator who typed "authentik" or "on" did not mean.
func TestLoadRefusesAnUnknownAuthMode(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_AUTH", "authentik")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an auth mode that is neither open nor proxy")
	}
}

// Proxy mode with neither way of vouching for the identity headers is a
// deployment where the browser cannot authenticate and only a token can — and
// no token can be minted without the browser.
func TestLoadRefusesProxyAuthWithNothingToTrust(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_AUTH", "proxy")
	t.Setenv("SCHALL_AUTH_PROXY_KEY", "")
	t.Setenv("SCHALL_AUTH_TRUSTED_PROXIES", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted proxy auth with no proxy key and no trusted network")
	}
}

func TestLoadReadsTrustedProxies(t *testing.T) {
	t.Setenv("SCHALL_DATABASE_URL", "postgres://example")
	t.Setenv("SCHALL_AUTH", "proxy")
	t.Setenv("SCHALL_AUTH_TRUSTED_PROXIES", "10.42.0.0/16, fd00::/8")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AuthTrustedProxies) != 2 {
		t.Fatalf("trusted proxies = %v, want two networks", cfg.AuthTrustedProxies)
	}

	t.Setenv("SCHALL_AUTH_TRUSTED_PROXIES", "10.42.0.1")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an address that is not a CIDR block")
	}
}
