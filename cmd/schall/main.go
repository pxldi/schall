package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/activity"
	"github.com/pxldi/schall/internal/anchor"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/config"
	"github.com/pxldi/schall/internal/coverart"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dedupe"
	"github.com/pxldi/schall/internal/deezer"
	"github.com/pxldi/schall/internal/downloads"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/jobs"
	"github.com/pxldi/schall/internal/labels"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/listens"
	"github.com/pxldi/schall/internal/loudness"
	"github.com/pxldi/schall/internal/lrclib"
	"github.com/pxldi/schall/internal/lyrics"
	"github.com/pxldi/schall/internal/matching"
	"github.com/pxldi/schall/internal/migrations"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/navidrome"
	"github.com/pxldi/schall/internal/newreleases"
	"github.com/pxldi/schall/internal/notify"
	"github.com/pxldi/schall/internal/peerchallenge"
	"github.com/pxldi/schall/internal/playlists"
	"github.com/pxldi/schall/internal/preview"
	"github.com/pxldi/schall/internal/recommendations"
	"github.com/pxldi/schall/internal/server"
	"github.com/pxldi/schall/internal/slskd"
	"github.com/pxldi/schall/internal/soundcloud"
	"github.com/pxldi/schall/internal/spectrum"
	"github.com/pxldi/schall/internal/tracksource"
	"github.com/pxldi/schall/internal/transcode"
	"github.com/pxldi/schall/internal/upgrade"
	"github.com/pxldi/schall/internal/uploads"
	"github.com/pxldi/schall/internal/waveform"
	"github.com/pxldi/schall/internal/weekly"
	"github.com/pxldi/schall/internal/wikipedia"
	"github.com/pxldi/schall/internal/youtube"
	"github.com/rs/zerolog"
)

// resolutionStartupBatch bounds how many unasked files a start queues. Each one
// is at least one rate-limited provider request, so startup schedules a day's
// worth of questions rather than a library's worth; the next scan continues.
const resolutionStartupBatch = 500

// databasePoolConfig is how Schall connects to PostgreSQL.
//
// JIT is off. PostgreSQL compiles a query to machine code when it estimates the
// query will cost more than jit_above_cost, and the estimate is not the time.
// The artists list is a page of 50 rows over 4,070 albums; the planner puts it
// at 1,832,310, past every JIT threshold, and LLVM then spent 1,027 ms of a
// 1,279 ms query on optimisation and emission. The same statement with JIT off
// runs in 203 ms and returns the same rows.
//
// Nothing Schall asks the database is an analytical query long enough to earn
// that compilation back, so it is off for the whole pool rather than for the
// one statement that exposed it.
func databasePoolConfig(databaseURL string) (*pgxpool.Config, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 1
	poolConfig.MaxConnLifetime = time.Hour
	poolConfig.ConnConfig.RuntimeParams["jit"] = "off"
	return poolConfig, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "schall: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := migrations.UpWithRetry(ctx, cfg.DatabaseURL, migrations.RetryOptions{
		Window:   cfg.DatabaseStartupTimeout,
		Interval: 2 * time.Second,
		OnRetry: func(attempt int, err error) {
			logger.Warn().
				Err(err).
				Int("attempt", attempt).
				Dur("window", cfg.DatabaseStartupTimeout).
				Msg("database is not accepting connections yet, retrying")
		},
	}); err != nil {
		return err
	}

	poolConfig, err := databasePoolConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	musicBrainzClient, err := musicbrainz.NewClient(musicbrainz.Options{
		BaseURL:   cfg.MusicBrainzBaseURL,
		UserAgent: cfg.MusicBrainzUserAgent,
	})
	if err != nil {
		return fmt.Errorf("configure MusicBrainz client: %w", err)
	}

	pathValidator, err := library.NewPathValidator(cfg.LibraryAllowedRoots)
	if err != nil {
		return fmt.Errorf("configure library paths: %w", err)
	}
	matchService := matching.NewService(pool, logger)
	store := db.New(pool)
	// One hub, shared by the workers that publish and the handler that streams,
	// so a change reaches an open interface without it having to ask.
	eventHub := events.NewHub()
	libraryScanner := library.NewScanner(pool, matchService).WithEvents(eventHub)
	// What files the library the way the layout says. It renames and updates the
	// rows those renames belong to; it decides nothing about the music.
	libraryMover := library.NewMover(pool, logger)
	// What writes the catalogue's account of a file into the file, for the music
	// that was in the library before an import was writing it. It decides
	// nothing either: it writes only where a mapping already says what the file
	// is.
	libraryTagger := library.NewTagger(pool, logger)
	// What measures how loud a file is, so the tag write above can tell a player
	// how far to turn each track down. It shares ffmpeg with the preview
	// transcoder and is optional in exactly the same way: an installation
	// without one measures nothing and writes no gain.
	loudnessMeasurer := loudness.NewMeasurer(cfg.FFmpegPath)
	// What takes a file back out again, once somebody has decided the library
	// holds the same music twice. It is given the matcher so the copy that stays
	// is judged again with the other one gone.
	libraryRemover := library.NewRemover(pool, logger).WithMatcher(matchService)
	// One gate for every slskd client this process builds. A client is made afresh
	// from the stored settings for each search, so the bounds on how much, how
	// fast and how repetitively this process searches have to live outside them
	// or they bound nothing.
	sourceGate := slskd.NewGate(0, 0, 0)
	// One builder behind every search, so a search reports its reply-arrival
	// curve the same way whichever lane asked for it.
	sourceBuilder := slskd.Builder{Gate: sourceGate, Logger: logger}
	// Where releases are pictured from, in the order of how well attached the
	// picture is: the identifier-keyed archive, then the release's own files, then
	// the name-keyed archive, which is the only guess among them. Artwork decides
	// nothing either way.
	artworkSources := coverart.Sources{
		Archive:     coverart.NewClient(coverart.Options{UserAgent: cfg.MusicBrainzUserAgent}),
		FromLibrary: coverart.NewLibrary(store),
		ByName:      coverart.NewITunesClient(coverart.Options{}),
	}
	// Where the words of a song come from. Free, keyless, and asked only about
	// recordings Schall has already proved: the artist, title, album and length
	// are the catalogue's, never the file's own tags.
	// The account's listening history, copied into the listens table for the
	// Overview to count. Read only; nothing is ever sent to ListenBrainz.
	listensService := listens.NewService(store, cfg.MusicBrainzUserAgent, logger)
	lyricsService := lyrics.NewService(store,
		lrclib.NewClient(lrclib.Options{UserAgent: cfg.MusicBrainzUserAgent}), logger)
	// The audio a want can be proven against. Deezer publishes a thirty-second
	// excerpt of most tracks it carries, free and with no key, and it is fetched
	// by the want's own ISRC — an exact key, never a text search — so the audio
	// that comes back is the audio of exactly the recording the want names. The
	// fingerprint of it is kept on the want. Nothing reads it to admit or refuse
	// a copy yet (ADR 0024).
	//
	// It shares the fpcalc path with the listener and nothing else: that one
	// fingerprints a file to ask AcoustID who it is and reads the opening two
	// minutes, while this reads a stated length, because an excerpt cut from the
	// middle of a song is not inside the opening two minutes of a copy of it.
	//
	// A want the distributor cannot answer for is anchored from YouTube through
	// yt-dlp (ADR 0029). With no yt-dlp on the machine such a want
	// waits on the retry ladder rather than being written off as having no
	// anchor, so nothing is lost until the program is installed.
	anchorService := anchor.NewService(store,
		deezer.NewClient(deezer.Options{}),
		youtube.NewFinder(youtube.NewClient(youtube.Options{Path: cfg.YtdlpPath})),
		chromaprint.NewFingerprinter(chromaprint.Options{FpcalcPath: cfg.FpcalcPath}),
		logger)
	// What tells the user that work has stopped and is waiting on them. It reads
	// its own settings on every use and sends nothing while they say it is off,
	// so an installation that never opens that section never talks to anybody.
	queueNotifier := notify.New(store, logger)
	// What tells the player the library changed. One instance, used by the job
	// that every write site queues and by the button that asks for it right
	// now, so both read the same settings and speak to the same address.
	playbackNotifier := navidrome.NewNotifier(store, logger)
	transferService := downloads.NewService(store, sourceBuilder, logger).WithEvents(eventHub)
	// A few Soulseek peers hold every transfer until a word is typed in their
	// private chat. This reads those messages and sends the word back, then asks
	// the peer for the files it refused. It changes nothing about what a file
	// has to prove once it arrives.
	peerChallenges := peerchallenge.NewService(store, sourceBuilder, transferService, logger)
	// Resolution reads a file's tags, and where they answer nothing it listens to
	// the audio. The listener is the same one the import path uses and reads the
	// same settings on every use, so an installation that never entered a key
	// resolves exactly the files it always did, from exactly the tags it read
	// before. It is wired here rather than beside the download inbox because a
	// file already in the library has nothing to do with downloading anything.
	identityService := identity.NewService(pool, musicBrainzClient, logger, matchService).
		WithListener(downloads.NewSettingsIdentifier(store.ImportSettings, cfg.FpcalcPath)).
		WithUploadReplacer(libraryRemover)
	searchService := downloads.NewSearcher(store, sourceBuilder, logger).WithEvents(eventHub)
	// SoundCloud is asked one question, through the endpoint it publishes
	// without a key: what is the track at this address called, and where is its
	// picture. It is never asked for audio, and it identifies no recording — a
	// source identity is a second key beside the MusicBrainz one (ADR 0026).
	// It is asked under the same user agent MusicBrainz is: one installation
	// saying who it is.
	soundCloudClient, err := soundcloud.NewClient(soundcloud.Options{
		UserAgent: cfg.MusicBrainzUserAgent,
	})
	if err != nil {
		return fmt.Errorf("set up the SoundCloud client: %w", err)
	}
	soundCloudService := soundcloud.NewService(pool, soundCloudClient, logger)
	// The same resolver answers what a file is, what an entry names, and whether
	// a fetched file is the recording a want was waiting for. One set of rules
	// decides all three, so none of them can be loosened on its own.
	resolver := identity.NewResolver(musicBrainzClient).WithAliases(store)
	acquisitionService := acquisition.NewService(store, logger).
		WithResolver(resolver).
		WithMatcher(matchService).
		// What writes down that a library file is the recording a want's copy
		// was proven to be. It is the same identity service the resolution job
		// and the review screen write through, so there is one place a file's
		// identity is decided and one place it is replaced.
		WithFiler(identityService).
		// What asks again about a file an anchor proved, and moves it onto a
		// recording once MusicBrainz proves one (ADR 0037 §6).
		WithSourceRechecker(identityService).
		// What carries out a proven upgrade want: comparing the copy that
		// arrived against the file below the floor it was raised for, and
		// replacing the file when the new copy is genuinely better. The same
		// remover the duplicates screen uses, so there is one deletion licence
		// and one deletion path rather than a second one for this feature.
		WithUpgrader(libraryRemover).
		// What reads the address a person keys a want to, and fingerprints
		// the excerpt taken from it as the want's anchor (ADR 0038). It uses
		// the yt-dlp the YouTube anchor uses; the artwork of the admitted file
		// is fetched through the SoundCloud client's plain image GET.
		WithTrackSources(tracksource.NewClient(tracksource.Options{Path: cfg.YtdlpPath}),
			chromaprint.NewFingerprinter(chromaprint.Options{FpcalcPath: cfg.FpcalcPath})).
		WithSourceNamer(tracksource.NewNamer(store, soundCloudClient, logger)).
		WithEvents(eventHub)
	playlistService := playlists.NewService(store, logger).
		WithEvents(eventHub)
	// ListenBrainz is MetaBrainz, so it is asked under the same identity
	// MusicBrainz is: one installation talking to one organisation.
	recommendationService := recommendations.NewService(store, cfg.MusicBrainzUserAgent, logger).
		WithRecordingProvider(musicBrainzClient)
	// Sync reads paths back across the boundary between Schall's mount and the
	// player's, so the map between them is handed over here rather than read
	// from settings: it describes the deployment, not the player. One syncer,
	// because the API asks it the read-only half of the same question the job
	// lane pushes with, and both have to be looking at the same boundary.
	playerSyncer := navidrome.NewSyncer(store, cfg.NavidromePathMap, logger)
	// The low-quality-file sweep: it reads the same per-format bit-rate floors
	// source preferences already sets and raises an ordinary want for a file
	// that falls under them. It ships switched off, in its own settings, and
	// the same instance answers both the settings screen and the job worker so
	// there is one sweep rather than two disagreeing about what "below the
	// floor" means.
	upgradeService := upgrade.NewService(store, logger)
	// The weekly playlist: the part of the recommendation engine that acts on
	// its own. It is also the only thing in Schall that deletes music without
	// somebody pressing a button, so what it may do is decided here, by what it
	// is handed. Without a remover it can announce a departure and never carry
	// it out; without a star reader the only keep signal is the Keep button, and
	// a refresh in that state removes nothing at all.
	//
	// It ships switched off. A person turns it on in the settings.
	weeklyPlaylist := weekly.NewService(store, logger).
		WithRecommender(recommendationService).
		WithStars(weekly.NewPlayerStars(navidrome.NewKeeps(store, cfg.NavidromePathMap, logger))).
		WithRemover(libraryRemover).
		WithPublisher(weeklyPublisher{syncer: playerSyncer}).
		WithEvents(eventHub)
	// The listening surface of the follow feed: one playlist holding the tracks
	// the library owns from the releases following somebody has turned up
	// lately. It moves playlist rows and nothing else — no file, no want and no
	// decision is in reach of it — so unlike the weekly playlist it needs no
	// remover, no star reader and no permission to run.
	newReleasesPlaylist := newreleases.NewService(store, logger).WithEvents(eventHub)
	jobsRepository := jobs.NewRepository(pool, matchService)
	labelRefresher := labels.New(store, musicBrainzClient, jobsRepository, logger)
	witnessFingerprinter := library.NewWitnessFingerprintSweep(
		pool, chromaprint.NewFingerprinter(chromaprint.Options{FpcalcPath: cfg.FpcalcPath}), logger)
	jobWorker := jobs.NewWorker(jobsRepository, musicBrainzClient, logger, libraryScanner).
		WithWitnessFingerprinter(witnessFingerprinter).
		WithTransferTracker(transferService).
		WithPeerChallengeAnswerer(peerChallenges).
		WithFileResolver(identityService).
		WithSourceSearcher(searchService).
		WithAcquisitionSweeper(acquisitionService).
		WithCoverArtSweeper(coverart.NewSweeper(store, artworkSources, logger).
			WithArtists(coverart.NewArtists(musicBrainzClient, artworkSources.Archive)).
			// And a few lines about who the artist is, reached down the same
			// chain the picture follows: MusicBrainz names the Wikidata entity,
			// the entity names the Wikipedia article, and the article is where
			// the words come from. Nobody is ever asked about an artist by name.
			WithBiographies(coverart.NewBiographies(
				musicBrainzClient, artworkSources.Archive,
				wikipedia.NewClient(wikipedia.Options{UserAgent: cfg.MusicBrainzUserAgent}))).
			// And the same pictures beside the music, where the playback server
			// reads them from. The archive is asked again for the full-size
			// front, because the cached copy is a thumbnail sized for Schall's
			// own pages and a phone opens a cover full-screen.
			WithDisk(coverart.NewDiskWriter(store, artworkSources.Archive, logger))).
		WithPlaylistImporter(playlistService).
		WithLabelRefresher(labelRefresher).
		WithFollowFeedSweeper(acquisition.NewFeed(store, acquisitionService, logger)).
		// The half of a standing "want what is missing" that nobody presses: a
		// release whose tracklist arrives hours after the press is wanted the
		// moment it lands.
		WithStandingWants(acquisition.NewStanding(store, acquisitionService, logger)).
		WithRecommendationSweeper(recommendationService).
		// The words of a song, written beside it as an .lrc file. LRCLIB is
		// asked by the catalogue's account of the recording and answers only
		// when the length agrees, so a song it has never heard of is silence
		// rather than somebody else's lyrics.
		WithLyrics(lyricsService).
		WithListens(listensService).
		// And the distributor's own excerpt of each wanted recording, kept as a
		// fingerprint on the want so a copy a stranger sends can be compared
		// against audio of the right recording.
		WithAnchorSweeper(anchorService).
		// And what the audio in each library file actually is: where the sound
		// stops, and what the file says made it. A lossless container whose
		// music stops at sixteen kilohertz was a lossy file once, whatever its
		// extension says. It is shown to a person choosing between two copies of
		// one recording and is read by nothing that admits or refuses a file — a
		// transcode of the right recording is still the right recording.
		WithSpectrumSweeper(spectrum.NewService(store,
			spectrum.NewReader(spectrum.Options{FFmpegPath: cfg.FFmpegPath}), logger)).
		// And the sweep that looks back at files acquired before a bit-rate
		// floor was set, or before it was raised, and raises an ordinary want
		// for each one under it. It ships switched off, in its own settings.
		WithUpgradeSweeper(upgradeService).
		// And a word to the user when a pass leaves a question behind. One
		// message per pass, never one per question, and a message that could
		// not be delivered costs nothing but a line in the log.
		WithQueueNotifier(queueNotifier).
		WithWeeklyPlaylistRefresher(weeklyPlaylist).
		WithNewReleasesPlaylistRefresher(newReleasesPlaylist).
		WithPlaybackNotifier(playbackNotifier).
		WithPlaylistSyncer(playerSyncer).
		WithLayoutMover(libraryMover).
		WithTagger(libraryTagger).
		// And how loud each file is, measured once and written into the file as
		// a replay gain tag, so a collection of masterings from four decades
		// plays at one volume. It needs ffmpeg, which is optional: without it
		// nothing is measured and no gain is written.
		WithLoudnessSweeper(library.NewLoudness(pool, loudnessMeasurer, logger)).
		WithEvents(eventHub)
	// The folder shape every import renders against, and the same one the
	// migration moves an existing library to. It is one function rather than one
	// per import path so that a download, an upload and a migration cannot
	// disagree about where a release belongs.
	//
	// Read per import, like the acoustic settings below, so a layout chosen later
	// applies to what arrives next rather than at the next restart. An
	// installation that never opened the setting has no row, and files music
	// exactly where it always has.
	libraryLayout := func(ctx context.Context) (library.Template, error) {
		row, err := store.LibraryLayoutSettings(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return library.ParseTemplate(library.DefaultTemplate)
		}
		if err != nil {
			return library.Template{}, err
		}
		return library.ParseTemplate(row.Template)
	}
	// What writes music into the library smaller than it arrived, when the
	// operator has asked for that. One service for both import paths, for the
	// same reason the layout is one function: a download and an upload of the
	// same album must not be filed in two different formats.
	//
	// The policy is read per import. It is off unless somebody turned it on, and
	// an installation with no ffmpeg files every file exactly as it arrived.
	shrinkCopies := transcode.NewService(func(ctx context.Context) (transcode.Policy, error) {
		row, err := store.ImportSettings(ctx)
		if err != nil {
			return transcode.Policy{}, err
		}
		return transcode.Policy{
			Enabled: row.TranscodeEnabled,
			Target:  row.TranscodeTarget,
			Bitrate: row.TranscodeBitrate,
			When:    row.TranscodeWhen,
		}, nil
	}, cfg.FFmpegPath, logger)
	// The button in Settings that shrinks what is already on disc, rather than
	// only what an import writes from here on. Same policy, same encoder, read
	// again per batch for the same reason: a setting changed mid-pass takes
	// effect on the pass's next batch rather than at the next restart.
	transcodeSweep := library.NewTranscodeSweep(pool, shrinkCopies, logger)
	jobWorker.WithTranscodeSweeper(transcodeSweep)
	// The duplicates screen's press, made on a schedule. It reads its own
	// setting and does nothing unless somebody switched it on.
	jobWorker.WithDuplicateSweeper(dedupe.NewDuplicateSweep(libraryRemover, pool))
	if cfg.DownloadInboxPath != "" {
		if info, err := os.Stat(cfg.DownloadInboxPath); err != nil {
			return fmt.Errorf("configure download inbox path: %w", err)
		} else if !info.IsDir() {
			return fmt.Errorf("configure download inbox path: path must be a directory")
		}
		if _, err := pathValidator.Validate(cfg.ImportLibraryPath); err != nil {
			return fmt.Errorf("configure import library path: %w", err)
		}
		// Acoustic verification reads its own settings on every use, so a key
		// entered later takes effect without a restart, and an installation
		// that never enters one is validated exactly as it was before.
		importer := downloads.NewImporter(
			store, cfg.DownloadInboxPath, cfg.ImportLibraryPath, logger,
		).WithAcousticIdentifier(
			downloads.NewSettingsIdentifier(store.ImportSettings, cfg.FpcalcPath),
		).WithVerifier(resolver).WithLayout(libraryLayout).
			// How loud each imported file is, measured as the import ends and
			// written into the file with everything else Schall proved about
			// it. A whole release imported at once is the one moment the record
			// can be levelled as a record, which is why it is measured here as
			// well as by the pass that walks the library.
			WithLoudness(loudnessMeasurer).
			// What measures a copy against the sample its want was anchored to. It
			// needs no key and no settings: the sample was already fetched and the
			// comparison is arithmetic on two fingerprints, so the only thing that
			// can switch it off is fpcalc not being installed.
			WithFingerprinter(
				chromaprint.NewFingerprinter(chromaprint.Options{FpcalcPath: cfg.FpcalcPath})).
			// And what a copy's audio is, as opposed to what its name claims. It
			// is written into the evidence for the person reading the review
			// queue and is passed to nothing that admits or refuses a copy.
			WithAudioMeasurer(spectrum.NewReader(spectrum.Options{FFmpegPath: cfg.FFmpegPath})).
			// And the shape of that audio, read once and kept on the copy, for the
			// card a person reads it on. It is a picture and decides nothing.
			WithWaveforms(waveform.NewReader(waveform.Options{FFmpegPath: cfg.FFmpegPath})).
			// What writes a smaller copy of a file that is already proven. Like
			// the acoustic check it reads its setting on every import, so
			// turning it on takes effect without a restart; unlike it, it runs
			// after every verdict and can change none of them.
			WithTranscoding(shrinkCopies)
		// A want is looked for through the same search a release run uses and
		// fetched through the same transfer path a folder chosen by hand takes.
		// Both are wired here rather than above, because looking for a copy nothing
		// could import would spend the user's bandwidth and a peer's upload slot on
		// bytes that could never reach the library. The listener decides the same
		// question one step further in: only the audio can admit a copy, so an
		// installation that cannot listen looks for nothing.
		acquisitionService.
			WithHunter(searchService).
			WithFetcher(transferService).
			WithListener(downloads.NewSettingsIdentifier(store.ImportSettings, cfg.FpcalcPath))
		transferService.WithImportQueue()
		jobWorker.WithDownloadImporter(importer)
		// What deletes the downloaded files nothing needs any more, by the rule
		// in ADR 0035. It is wired beside the importer because it
		// works on the same folder, and it runs only when somebody asks.
		//
		// It is given the managed library and the allowed roots so it can refuse
		// to run against an inbox that overlaps either. The allowed roots are
		// enough on their own to bound every music folder, now and later:
		// PathValidator refuses a root outside them, so no folder added
		// afterwards can escape this check.
		jobWorker.WithInboxCleaner(downloads.NewInboxCleaner(
			store, cfg.DownloadInboxPath,
			append([]string{cfg.ImportLibraryPath}, cfg.LibraryAllowedRoots...), logger))
		if err := store.QueuePendingDownloadImports(ctx); err != nil {
			return fmt.Errorf("queue pending download imports: %w", err)
		}
	}

	// The other acquisition route: music the user already owns, arriving through
	// a browser rather than from a peer. It is wired independently of the
	// download path, because an installation may only ever use one of them.
	var uploadStaging *uploads.Staging
	if cfg.UploadStagingPath != "" {
		// The managed library is where an upload ends up, so it has to be a place
		// a scan will walk. The download path checks the same thing for the same
		// reason; an installation that only uploads still needs it checked.
		if _, err := pathValidator.Validate(cfg.ImportLibraryPath); err != nil {
			return fmt.Errorf("configure import library path: %w", err)
		}
		uploadStaging, err = uploads.NewStaging(cfg.UploadStagingPath, logger)
		if err != nil {
			return fmt.Errorf("configure upload staging path: %w", err)
		}
		// Checked against the folders that exist rather than assumed from the
		// configuration: a staging folder a scan could walk into would have that
		// scan index half-written uploads as music the library holds. The allowed
		// roots are included because they bound every folder that could be added
		// later, so a staging path outside all of them stays outside every root
		// this installation will ever have.
		roots, err := store.ListLibraryRoots(ctx)
		if err != nil {
			return fmt.Errorf("read the music folders: %w", err)
		}
		libraryPaths := append([]string{}, cfg.LibraryAllowedRoots...)
		for _, root := range roots {
			libraryPaths = append(libraryPaths, root.Path)
		}
		if err := uploads.EnsureOutsideLibrary(uploadStaging.Root(), libraryPaths); err != nil {
			return fmt.Errorf("configure upload staging path: %w", err)
		}
		jobWorker.WithUploadImporter(
			// No WithTranscoding here: an upload has no verdict of its own, so
			// there is nothing for a re-encode to run safely after. The
			// setting reaches this music once it has a resolved identity,
			// through the library sweep every other file goes through.
			uploads.NewImporter(uploadStaging, cfg.ImportLibraryPath, store, logger).
				WithLayout(libraryLayout),
		)
		// The same service Library's "This is a SoundCloud track" dialog uses.
		// An upload that arrived with an address is named through it too, once
		// the scan below gives the file a row to name.
		jobWorker.WithSoundCloud(soundCloudService)
		// An upload whose job was never queued — a restart between the last byte
		// landing and the row being written — is a folder nothing would ever look
		// at again. Asking about every staged folder is idempotent: an upload that
		// already has a job keeps it.
		staged, err := uploadStaging.List()
		if err != nil {
			return fmt.Errorf("read the upload staging folder: %w", err)
		}
		for _, upload := range staged {
			// A restart between accepting an upload and queuing its import loses
			// whatever address the browser sent — the request that carried it is
			// gone. The file lands unnamed and is named from its own row, the
			// same as an upload nobody sent an address for.
			if _, err := store.QueueUploadImport(
				ctx, upload.ID, upload.Names(), upload.TotalBytes(), nil,
			); err != nil {
				return fmt.Errorf("queue staged upload %s: %w", upload.ID, err)
			}
		}
	}

	// A restart must not lose the files a scan queued but the worker never got
	// to, and a library that predates resolution has never been asked about at
	// all. Both are the same bounded batch of work.
	if queued, err := store.QueueFileResolutions(ctx, resolutionStartupBatch); err != nil {
		return fmt.Errorf("queue file resolutions: %w", err)
	} else if queued > 0 {
		logger.Info().Int64("files", queued).Msg("queued files for identity resolution")
	}

	// A want is due at a time it recorded for itself, and a restart is exactly
	// when that time has quietly passed. Queueing one sweep picks up everything
	// that fell due while Schall was down; a sweep with nothing to do queues
	// nothing after itself and costs one query.
	if err := store.QueueAcquisitionSweep(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue acquisition sweep: %w", err)
	}

	// And one picture sweep, which finds the releases nothing has looked for a
	// cover of — every release, the first time — and then schedules itself. It is
	// the only thing that asks an archive on its own initiative, so the rate a
	// catalogue is pictured at is a decision made in one place.
	//
	// Which is why this asks for a pass to exist rather than for one now. Both
	// this sweep and the follow feed below run six hours apart and are queued
	// again here every time the process starts; moving them to now would put that
	// rate in the hands of whoever deploys (issue #334). A pass whose time went by
	// while Schall was down is due already and runs as soon as the worker starts,
	// so an installation that was switched off still catches up.
	if err := store.EnsureCoverArtSweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue cover art sweep: %w", err)
	}

	// And one lyrics sweep, which walks the library a page at a time writing the
	// words of each song beside it, for the phone to scroll through. Same rule as
	// the picture sweep: the pass already on the books keeps its time and its
	// position, so restarting does not restart the walk.
	if err := store.EnsureLyricsSweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue lyrics sweep: %w", err)
	}

	// And one genre backfill, which queues an artist refresh for every artist in
	// a catalogue that predates genres. Same rule again: a pass already on the
	// books keeps its time, so restarting does not queue the batch twice.
	if err := store.EnsureGenreSweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue genre sweep: %w", err)
	}

	// And one preview anchor sweep, which fetches the distributor's excerpt of
	// each wanted recording. Same rule again: the pass already on the books keeps
	// its time, so a pod replaced while Deezer was rate-limiting does not cancel
	// the wait it was serving.
	if err := store.EnsurePreviewAnchorSweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue preview anchor sweep: %w", err)
	}

	// And one pass over the held copies whose audio has never been measured. A
	// held copy is a file a peer sent that nothing could decide about, and the
	// measurement lets the review queue show the copies that are one piece of
	// audio as one row. It decides nothing, so a run that never happens costs a
	// longer queue and never a wrong answer.
	if err := store.EnsureCopyFingerprintSweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue copy fingerprint sweep: %w", err)
	}
	if err := jobsRepository.QueueLibraryWitnessFingerprintSweep(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue library witness fingerprint sweep: %w", err)
	}

	// And one loudness pass, which measures how loud the library's files are so
	// a player can even the collection out. Same rule again: the pass already on
	// the books keeps its time, and a pass measures a small batch and schedules
	// its own successor, so a restart neither starts the walk again nor loses
	// its place.
	if err := store.QueueLoudnessSweep(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue loudness sweep: %w", err)
	}

	// And one audio spectrum sweep, which decodes a few seconds of each library
	// file to find where its music stops. Same rule again: the pass on the books
	// keeps its time, so a restart does not send the whole library through the
	// decoder a second time.
	if err := store.EnsureSpectrumSweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue audio spectrum sweep: %w", err)
	}

	// And one upgrade sweep, which walks the library's files against the
	// stored quality floor. Switched off until somebody turns it on, so this
	// queues a pass that finds nothing rather than one that deletes music on
	// its first day.
	if err := store.EnsureUpgradeSweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue upgrade sweep: %w", err)
	}

	// And one follow feed sweep, which asks about the followed artists nobody has
	// looked at for a day and wants what they have released since being followed.
	if err := store.EnsureFollowFeedSweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue follow feed sweep: %w", err)
	}

	// And one weekly pass over the library's tags. The per-file write already
	// covers a file the moment it is matched, so this is for the files it
	// cannot reach: MusicBrainz corrects a release date or an artist credit
	// after a file was written, and the file keeps the old value until somebody
	// presses "Write tags" or this pass comes round to it. Same rule again: a
	// pass already queued, by the button or by last week's sweep, keeps the
	// time it holds.
	if err := libraryTagger.EnsureLibrarySweepQueued(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue library tagging sweep: %w", err)
	}

	// A recommendation pass is useful only when an enabled account exists. Once
	// seeded it schedules every later pass itself; an installation that has not
	// connected ListenBrainz makes no external request and leaves no failing job
	// behind merely to say that it has not been configured.
	//
	// Starting up is not a reason to sweep. It is a reason to make sure a pass
	// exists, which is why this asks for one only when none is queued and none
	// is running: a pass waiting out the thirty minutes it chose after a service
	// answered slowly keeps that wait across a restart.
	listenBrainzSettings, err := store.ListenBrainzSettings(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read ListenBrainz settings for recommendation sweep: %w", err)
	}
	if err == nil && listenBrainzSettings.Enabled {
		if err := store.EnsureRecommendationSweepQueued(ctx, time.Now()); err != nil {
			return fmt.Errorf("queue recommendation sweep: %w", err)
		}
		// And the listening history, under the same rule: only an enabled
		// account is asked, and a sync already waiting keeps its time.
		if err := store.EnsureListensSyncQueued(ctx, time.Now()); err != nil {
			return fmt.Errorf("queue listens sync: %w", err)
		}
	}

	// A refresh left open by a restart would block every later one, because one
	// refresh at a time is a database rule. Closing it says plainly that nobody
	// knows how far it got.
	if err := weeklyPlaylist.CloseAbandonedRuns(ctx); err != nil {
		return fmt.Errorf("close weekly playlist runs left open: %w", err)
	}

	// And one weekly refresh, only where somebody switched the weekly playlist
	// on. It removes music, so an installation that never opened the setting
	// leaves no job behind at all — not even one that would decide to do
	// nothing.
	weeklySettings, err := store.WeeklyPlaylistSettings(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read weekly playlist settings: %w", err)
	}
	if err == nil && weeklySettings.Enabled {
		if err := store.QueueWeeklyPlaylistRefresh(ctx, time.Now()); err != nil {
			return fmt.Errorf("queue weekly playlist refresh: %w", err)
		}
	}

	// And one pass over the player, which adopts the lists it holds that Schall
	// has not joined and then pushes every want list. It is queued here for the
	// same reason as the others and for one more: it is the only pass nothing
	// else asks for. An import queues a pass over the list it just reconciled
	// and a press queues one now, but a copy the acquisition loop fetched
	// overnight belongs to no import and nobody pressed anything for it. This is
	// what carries it to the player, and it schedules every later one.
	//
	// An installation with no player configured finds that out in the pass and
	// queues the next; nothing here needs to know whether one exists.
	if err := store.QueueNavidromePlaylistSweep(ctx, time.Now()); err != nil {
		return fmt.Errorf("queue navidrome playlist sweep: %w", err)
	}

	// Five lanes over one queue. Looking for music is mostly waiting — a
	// Soulseek search collects replies until a fixed window closes — and
	// following a transfer to its import has to happen while the transfer is
	// still there to follow. Preview anchors and copy judging also have work that
	// must not wait behind the general queue. No lane is a pool: see
	// jobs.Acquisition.
	var workers sync.WaitGroup
	for _, lane := range []jobs.Lane{
		jobs.Acquisition(), jobs.Transfers(), jobs.Anchors(), jobs.Judging(), jobs.General(),
	} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			jobWorker.RunLane(ctx, lane)
		}()
	}

	apiOptions := []server.Option{
		server.WithLibraryPaths(pathValidator),
		server.WithLayoutMover(libraryMover),
		server.WithLibraryTagger(libraryTagger),
		server.WithTranscodeSweep(transcodeSweep),
		server.WithFileRemover(libraryRemover),
		server.WithMatchResolver(matchService),
		server.WithIdentityResolver(identityService),
		server.WithSoundCloud(soundCloudService),
		server.WithLabels(store),
		server.WithLabelSearch(musicBrainzClient),
		server.WithSourceProvider(sourceBuilder),
		// The same gate, read rather than used: it is the one thing that knows
		// searching has stopped because Soulseek went quiet on this account, and
		// the Overview is where the other reason searching stops is already said.
		server.WithSearchPause(sourceGate),
		// The archive is asked by identifier only, and only for a release the
		// catalogue has already resolved. It decides nothing.
		server.WithCoverArt(artworkSources),
		// And the same client again, for the cover somebody sets by hand from an
		// address they pasted. It is the archive's client because the fetch is
		// the archive's fetch — the same size bound, the same user agent, the
		// same reading of what came back — pointed somewhere Schall did not
		// choose.
		server.WithCoverPictures(artworkSources.Archive),
		server.WithTransferController(transferService),
		server.WithPeerChallenges(peerChallenges),
		server.WithAcquisitionTargets(acquisitionService),
		server.WithSourceKeys(acquisitionService),
		server.WithPlaylists(playlistService),
		server.WithPlayerPairing(playerSyncer),
		server.WithPlaybackNotifier(playbackNotifier),
		server.WithRecommendationSource(recommendationService),
		server.WithNotifier(queueNotifier),
		// The same service, asked the other question: not "is the account
		// reachable" but "what did the last sweep leave behind, and what may be
		// shown of it now".
		server.WithRecommendations(recommendationService),
		server.WithOverview(store),
		server.WithWeeklyPlaylist(weeklyPlaylist),
		server.WithNewReleasesPlaylist(newReleasesPlaylist),
		server.WithUpgrades(upgradeService),
		server.WithActivity(activity.New(pool)),
		server.WithDuplicateResolution(dedupe.NewSettings(pool)),
		server.WithDownloadInbox(cfg.DownloadInboxPath),
		server.WithPreviews(preview.New(cfg.FFmpegPath, cfg.PreviewCachePath, logger)),
		// For the copies held before the importer started keeping a waveform.
		server.WithWaveforms(waveform.NewReader(waveform.Options{FFmpegPath: cfg.FFmpegPath})),
		// Named here so that turning re-encoding on can be refused on a machine
		// with no encoder, rather than accepted and quietly never done.
		server.WithEncoder(cfg.FFmpegPath),
		server.WithEventHub(eventHub),
		server.WithAuth(server.AuthSettings{
			Mode:           cfg.AuthMode,
			ProxyKey:       cfg.AuthProxyKey,
			TrustedProxies: cfg.AuthTrustedProxies,
		}),
	}
	// Said once at startup rather than per request. Open is the default and is
	// right for development, and it is the wrong thing to discover on a machine
	// somebody put on the internet.
	if cfg.AuthMode == config.AuthOpen {
		logger.Warn().Msg("SCHALL_AUTH is open: every request is served without a credential")
	}
	// Appended rather than always passed, because a nil staging folder handed to
	// an interface parameter is an interface that is not nil, and the routes
	// would then answer as though uploading worked.
	if uploadStaging != nil {
		apiOptions = append(apiOptions, server.WithUploadStaging(uploadStaging))
	}
	// Appended for the same reason, and only where there is an inbox to clean:
	// an installation with no download folder has nothing to delete, and says so
	// rather than offering a button that cannot work.
	if cfg.DownloadInboxPath != "" {
		apiOptions = append(apiOptions, server.WithInboxCleanups(store))
	}

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           server.NewAPI(store, pool, musicBrainzClient, logger, apiOptions...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info().Str("address", cfg.HTTPAddress).Msg("Schall is ready")
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			stop()
			workers.Wait()
			return fmt.Errorf("serve HTTP: %w", err)
		}
	case <-ctx.Done():
		logger.Info().Msg("shutting down")
	}

	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	workers.Wait()
	return nil
}

// weeklyPublisher sends the weekly list to the player.
//
// It is the ordinary playlist sync, which answers with a result nobody here
// reads: whether the list reached the player is what matters to a refresh, and
// what it did track by track is the sync's own account, on the playlist screen.
type weeklyPublisher struct {
	syncer *navidrome.Syncer
}

func (publisher weeklyPublisher) SyncPlaylist(ctx context.Context, playlistID uuid.UUID) error {
	_, err := publisher.syncer.SyncPlaylist(ctx, playlistID)
	return err
}

func newLogger(levelName string) zerolog.Logger {
	level, err := zerolog.ParseLevel(levelName)
	if err != nil {
		level = zerolog.InfoLevel
	}
	return zerolog.New(os.Stdout).
		Level(level).
		With().
		Timestamp().
		Str("service", "schall").
		Logger()
}
