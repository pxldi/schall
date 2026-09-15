package downloads

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// SearchStore is the part of the database a bulk search needs.
type SearchStore interface {
	ClaimSourceSearch(context.Context, uuid.UUID, uuid.UUID) (db.SourceSearchAlbum, error)
	RecordSourceSearch(context.Context, db.RecordSourceSearchParams) error
	SlskdSettings(context.Context) (db.SlskdSettingsRow, error)
	// What the user said to prefer among the copies on offer, and what never to
	// fetch. An installation that has said nothing reads as the empty answer.
	SourcePreferences(context.Context) (db.SourcePreferencesRow, error)
	// The rest is what a run needs to record a download itself. It goes through
	// exactly the calls the per-release endpoint uses, including the duplicate
	// evidence, so there is no second way into download_requests.
	AlbumDuplicateEvidence(context.Context, uuid.UUID) (db.DuplicateEvidence, error)
	CreateDownloadRequest(context.Context, db.CreateDownloadRequestParams) (uuid.UUID, bool, error)
	MarkSourceSearchAutoRequested(context.Context, uuid.UUID, uuid.UUID, time.Time) error
}

// emptyRetryDelay is how long a search that found nothing waits before asking
// its broadest rung once more, so the repeat reaches a slightly different set
// of peers rather than echoing the first search immediately.
const emptyRetryDelay = 5 * time.Second

// searchHeadroom is the part of a want's search budget reserved for work around
// the searches themselves: admission through the shared provider gate, walking
// and ranking between rungs, and ordinary scheduling variance. Every reply
// window, and the pause a fruitless ladder waits out before it asks again, are
// counted separately; the provider rechecks its own allowance after admission,
// so headroom never makes an underfunded rung look affordable.
const searchHeadroom = 10 * time.Second

// Searcher looks for sources for one release at a time, on the worker's behalf,
// and records what it found. The ranked candidates are stored as the provider
// offered them.
//
// It chooses only where there is nothing to choose. A run started with
// permission to may record the download for a release whose best candidate has
// every catalogue track confirmed by name and length, and for no other. Which
// of several plausible folders is worth having, and whether to acquire music
// the library already holds, both stay questions for the person who asked.
type Searcher struct {
	store     SearchStore
	providers sources.Builder
	logger    zerolog.Logger
	notices   *events.Hub
	// emptyRetryDelay is a field rather than the constant so a test can drop
	// the pause without waiting it out.
	emptyRetryDelay time.Duration
	// headroom is a field for the same reason. It is ten seconds for a real
	// network, and a test that has to watch the second look happen would
	// otherwise have to spend ten seconds of a reply window to reach it.
	headroom time.Duration
}

func NewSearcher(store SearchStore, providers sources.Builder, logger zerolog.Logger) *Searcher {
	return &Searcher{
		store: store, providers: providers, logger: logger,
		emptyRetryDelay: emptyRetryDelay,
		headroom:        searchHeadroom,
	}
}

// WithEvents registers the hub that tells the interface a run moved on, so a
// review screen fills in as results land rather than on a timer.
func (searcher *Searcher) WithEvents(hub *events.Hub) *Searcher {
	searcher.notices = hub
	return searcher
}

// Search finds sources for one release of a run and records the outcome.
//
// The only error it returns is one worth trying again: an unreachable provider.
// Finding nothing is an answer, and it is recorded as one, because a release no
// peer is sharing must not sit in the queue being asked about forever.
func (searcher *Searcher) Search(ctx context.Context, runID, albumID uuid.UUID) error {
	album, err := searcher.store.ClaimSourceSearch(ctx, runID, albumID)
	if err != nil {
		return err
	}

	provider, err := searcher.provider(ctx)
	if err != nil {
		return err
	}

	// The same normalization the per-release search uses, so a bulk search and
	// a manual one look for the release in exactly the same words.
	queryTexts := sources.CatalogueQueryTexts(album.ArtistName, album.AlbumTitle)
	query := sources.Query{
		ExpectedTrackCount: int(album.TrackCount),
		Tracks:             queryTracks(album.Tracks),
	}
	if len(queryTexts) == 0 || queryTexts[0] == "" {
		return searcher.record(ctx, runID, albumID, "none", "", nil, nil,
			"the release name contains nothing that could be searched for")
	}

	candidates, refused, text, err := searcher.ask(ctx, provider, query, queryTexts)
	if err != nil {
		// A provider that could not be searched has told us nothing about this
		// release, so the job is left to retry rather than the result recorded
		// as though no peer had it.
		return err
	}

	// An empty result is asked again before it is believed. Soulseek search is
	// a broadcast that collects replies for a bounded window, so finding
	// nothing means nobody answered in time rather than nobody has it: a
	// release recorded as unavailable here has been found in full by replaying
	// the identical query. This does not remove false negatives, it makes them
	// rarer, and it costs nothing on a release that was found first time.
	if len(candidates) == 0 {
		if !wait(ctx, searcher.emptyRetryDelay) {
			return ctx.Err()
		}
		if candidates, refused, text, err = searcher.ask(
			ctx, provider, query, queryTexts,
		); err != nil {
			return err
		}
		if len(candidates) > 0 {
			searcher.logger.Info().
				Str("album_id", albumID.String()).Str("query", text).
				Int("candidates", len(candidates)).
				Msg("a repeated search found sources the first attempt did not")
		}
	}

	query.Text = text
	status := "found"
	if len(candidates) == 0 {
		status = "none"
	}
	if err := searcher.record(
		ctx, runID, albumID, status, query.Text, candidates, refused, "",
	); err != nil {
		return err
	}

	// Recording the download is a separate step on purpose. The search has been
	// answered and stored by now, so a request that cannot be recorded leaves
	// the release waiting in the review screen — which is where it would have
	// been anyway — rather than undoing an answer that was correct.
	if album.AutoRequest {
		searcher.autoRequest(ctx, runID, album, candidates)
	}
	return nil
}

// autoRequest records the download for a release whose best candidate leaves
// nothing to decide, when the run was started with permission to.
//
// "Nothing to decide" is a high bar and deliberately not the score: every
// catalogue track must be confirmed by a file carrying its name and running to
// its length. The score measures how good a copy is and has no term for which
// release it holds, so acting on it would be acting on nothing.
//
// A release the library already holds part of is never taken automatically,
// however well corroborated. That question is about the user's own files, and
// it has an answer only they can give.
//
// Nor is a release whose files the library will never index: APE, WavPack,
// ALAC, DFF and WMA all rank as lossless (sources.losslessExtensions) but sit
// outside library.supportedExtensions, so a scan never makes a row for what
// this would fetch. An AIFF fetched this way was proven, imported, and then
// waited nine hours for a library row no scan would ever create, before AIFF
// was added to the supported list; this is that failure's unfixed twin. The
// want-hunt path already asks the same question before it chooses an offer
// (acquisition.holdable); this is the one that did not.
func (searcher *Searcher) autoRequest(
	ctx context.Context, runID uuid.UUID, album db.SourceSearchAlbum, candidates []sources.Candidate,
) {
	if len(candidates) == 0 {
		return
	}
	best := candidates[0]
	if !best.Match.Complete() {
		return
	}
	if !filesHoldable(best.Files) {
		searcher.logger.Info().Str("album_id", album.AlbumID.String()).
			Str("format", sources.NormalizeExtension(best.Format)).
			Msg("a corroborated source was left for review because the library cannot hold its format")
		return
	}

	duplicates, err := searcher.store.AlbumDuplicateEvidence(ctx, album.AlbumID)
	if err != nil {
		searcher.logger.Error().Err(err).Str("album_id", album.AlbumID.String()).
			Msg("could not check duplicates before recording a download automatically")
		return
	}
	if duplicates.Any() {
		searcher.logger.Info().Str("album_id", album.AlbumID.String()).
			Int("owned_tracks", duplicates.OwnedTrackCount).
			Int("unresolved_files", duplicates.UnresolvedCount).
			Msg("a corroborated source was left for review because the library already holds some of it")
		return
	}

	params := db.CreateDownloadRequestParams{
		AlbumID:            album.AlbumID,
		Provider:           best.Provider,
		SourceUsername:     best.Username,
		SourceDirectory:    best.Directory,
		FileCount:          int32(len(best.Files)),
		ExpectedTrackCount: album.TrackCount,
		Format:             sources.NormalizeExtension(best.Format),
		Score:              best.Score,
		Reasons:            best.Reasons,
	}
	for _, file := range best.Files {
		params.TotalSizeBytes += file.SizeBytes
		params.Files = append(params.Files, db.DownloadRequestFile{
			Path: file.Path, Name: file.Name,
			Extension: sources.NormalizeExtension(file.Extension),
			SizeBytes: file.SizeBytes, BitRate: file.BitRate,
			DurationSeconds: file.DurationSeconds,
		})
	}
	if best.AverageBitRate > 0 {
		params.AverageBitRate = pgtype.Int4{Int32: int32(best.AverageBitRate), Valid: true}
	}

	id, created, err := searcher.store.CreateDownloadRequest(ctx, params)
	if err != nil {
		searcher.logger.Error().Err(err).Str("album_id", album.AlbumID.String()).
			Msg("could not record a download automatically")
		return
	}
	if err := searcher.store.MarkSourceSearchAutoRequested(
		ctx, runID, album.AlbumID, time.Now(),
	); err != nil {
		searcher.logger.Error().Err(err).Str("album_id", album.AlbumID.String()).
			Msg("recorded a download automatically but could not mark the search as having done so")
	}
	if created {
		searcher.logger.Info().
			Str("album_id", album.AlbumID.String()).Str("request_id", id.String()).
			Str("username", best.Username).Str("directory", best.Directory).
			Int("tracks", best.Match.Expected).
			Msg("recorded a download automatically: every catalogue track is confirmed")
	}
	searcher.notices.Publish(events.TopicDownloads)
	searcher.notices.Publish(events.TopicSourceSearches)
}

// filesHoldable reports whether every file of a candidate is one a library
// scan would index, asking the same question of the same names a peer
// advertised that acquisition.holdable asks for a want's own offers.
func filesHoldable(files []sources.File) bool {
	for _, file := range files {
		name := file.Name
		if name == "" {
			name = file.Path
		}
		if !library.CanHold(name) {
			return false
		}
	}
	return true
}

// Fail records that the search could not be run, once retrying has stopped, so
// a release is never left looking as though nobody had got to it yet.
func (searcher *Searcher) Fail(ctx context.Context, runID, albumID uuid.UUID, reason string) error {
	return searcher.record(ctx, runID, albumID, "failed", "", nil, nil, reason)
}

func (searcher *Searcher) record(
	ctx context.Context, runID, albumID uuid.UUID,
	status, query string, candidates []sources.Candidate, refused []sources.Refused,
	failure string,
) error {
	err := searcher.store.RecordSourceSearch(ctx, db.RecordSourceSearchParams{
		RunID:      runID,
		AlbumID:    albumID,
		Status:     status,
		Query:      query,
		Candidates: storedCandidates(candidates),
		Refused:    storedRefusals(refused),
		Error:      failure,
		SearchedAt: time.Now(),
	})
	if err != nil {
		return err
	}
	searcher.notices.Publish(events.TopicSourceSearches)
	return nil
}

func (searcher *Searcher) provider(ctx context.Context) (sources.Provider, error) {
	row, err := searcher.store.SlskdSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !row.Enabled {
		return nil, sources.ErrNotConfigured
	}
	return searcher.providers.Build(sources.Settings{
		BaseURL:       row.BaseURL,
		APIKey:        row.APIKey,
		SearchTimeout: time.Duration(row.SearchTimeoutSeconds) * time.Second,
	})
}

// ask walks the query ladder and stops at the first phrase that finds anything,
// returning what it found and the phrase that found it. A phrase that finds
// nothing is not an error: the ladder exists so a narrower spelling can be
// tried after a broader one.
// The refusals come back with the answer rather than being added to a running
// total: the same peers answer a repeated search, so a union of two asks counts
// the same refused copy twice. What is reported is what the stored answer left
// out.
func (searcher *Searcher) ask(
	ctx context.Context, provider sources.Provider, query sources.Query, texts []string,
) ([]sources.Candidate, []sources.Refused, string, error) {
	candidates, refused, text, _, err := searcher.walk(ctx, provider, query, texts, 0, searcher.preferences(ctx))
	return candidates, refused, text, err
}

func (searcher *Searcher) askWithinBudget(
	ctx context.Context, provider sources.Provider, query sources.Query, texts []string,
) ([]sources.Candidate, []sources.Refused, string, bool, error) {
	return searcher.walk(ctx, provider, query, texts, provider.SearchBudget(), searcher.preferences(ctx))
}

func (searcher *Searcher) askWithinBudgetWithPreferences(
	ctx context.Context, provider sources.Provider, query sources.Query, texts []string,
	preferences sources.Preferences,
) ([]sources.Candidate, []sources.Refused, string, bool, error) {
	return searcher.walk(ctx, provider, query, texts, provider.SearchBudget(), preferences)
}

func (searcher *Searcher) walk(
	ctx context.Context, provider sources.Provider, query sources.Query, texts []string,
	perRung time.Duration, preferences sources.Preferences,
) ([]sources.Candidate, []sources.Refused, string, bool, error) {
	var used string
	var refused []sources.Refused
	for _, text := range texts {
		if perRung > 0 && !canFinishSearch(ctx, perRung) {
			return nil, refused, used, true, nil
		}
		query.Text, used = text, text
		candidates, err := provider.Search(ctx, query)
		if err != nil {
			return nil, refused, used, false, err
		}
		// The user's own order is laid over the provider's here rather than
		// inside it, so one rule decides what may be fetched for every search
		// there is, and the copies it refuses are kept rather than dropped: a
		// want offered six copies in a format the user refuses has not been
		// offered nothing, and only the refusals can say so.
		kept, turnedAway := sources.Prefer(candidates, preferences)
		refused = append(refused, turnedAway...)
		if len(kept) > 0 {
			return kept, refused, used, false, nil
		}
	}
	return nil, refused, used, false, nil
}

// preferences reads what the user said to prefer. A read that fails is answered
// with the empty preferences and a warning: a settings row that cannot be read
// is a reason to rank as Schall always did, never a reason to abandon a search
// somebody is waiting on.
func (searcher *Searcher) preferences(ctx context.Context) sources.Preferences {
	row, err := searcher.store.SourcePreferences(ctx)
	if err != nil {
		searcher.logger.Warn().Err(err).
			Msg("could not read the stored format preferences; ranking as configured by default")
		return sources.Preferences{}
	}
	floors := make(map[string]int, len(row.FormatMinimumBitRate))
	for format, floor := range row.FormatMinimumBitRate {
		floors[format] = int(floor)
	}
	return sources.Preferences{
		Preferred:            row.Preferred,
		Unacceptable:         row.Unacceptable,
		MinimumBitRate:       int(row.MinimumBitRate),
		FormatMinimumBitRate: floors,
	}
}

// searchBudget is the whole allowance one want gets: a reply window for every
// rung of its ladder, the pause a fruitless ladder waits out, one more reply
// window for the broadest rung asked again after that pause, and headroom for
// the work around all of it.
//
// The repeat is paid for here rather than taken out of headroom. A rung that
// finds nothing spends its complete reply window, because the window is how long
// Schall waits for peers to reply, so a ladder that finds nothing spends every
// rung's window exactly and arrives at the pause with headroom alone. Headroom
// is ten seconds and a reply window (perRung, provider.SearchBudget()) is 368
// at the default settings -- the dequeue wait plus the reply window plus the
// HTTP allowance -- so the repeat was refused every time -- in the only case
// it exists for, which is a want nobody answered (issue #301). Paying for it
// costs a fruitless want the pause and one more window, 373 seconds at the
// default settings, held longer in the acquisition lane. It buys the one
// thing that tells silence from an answer.
//
// Nothing here changes how fast searches go out. The budget is a deadline, and
// the pause between the two asks is added to it, never taken from it: asking the
// same phrase again too soon is what earns this instance its Soulseek bans.
func (searcher *Searcher) searchBudget(rungs int, perRung time.Duration) time.Duration {
	return time.Duration(rungs+1)*perRung + searcher.emptyRetryDelay + searcher.headroom
}

// canFinishSearch admits a rung only while the caller can still pay the
// provider's whole allowance. A context without a deadline is unbounded.
func canFinishSearch(ctx context.Context, budget time.Duration) bool {
	deadline, bounded := ctx.Deadline()
	return !bounded || time.Until(deadline) >= budget
}

// broadestRung is the one phrase a want's empty search asks again: the rung with
// the fewest tokens to match, handed back as a ladder of one so ask walks it
// unchanged.
//
// Breadth is counted in tokens because tokens are what the network matches on.
// Soulseek requires every token of the phrase to appear in the peer's path, so a
// phrase reaches fewer peers with each token it carries, and sources.query is
// built on that fact end to end: the release-kind label dropped because that one
// extra token "can turn a precise query into no results", the title's qualifier
// dropped because seven tokens in a row find nothing, the artist dropped last
// because without it the phrase "matches every peer who has a file of that name".
//
// It is not simply the last rung, which is the easy reading and the wrong one.
// The ladder ends with the title on its own, qualifier and all, so a want for
// "Lvstlove (Night Drive At 177mph Mix)" ends on five tokens while a rung above
// it asks two -- "Anetha Lvstlove", the plain title, which is the phrase that
// reaches the folder a peer is actually sharing. Repeating the last rung there
// would repeat one of the narrowest phrases the want has, which is worse than
// repeating all of them. Where two rungs are the same width the later is taken,
// deferring to the ladder's own order: a rung is appended as a broadening of what
// stands above it, and for a want whose artist Soulseek blacklists -- "Gorillaz"
// is the reported case -- the title alone is the only rung that can answer at all.
//
// The count does not separate every pair, and one thing is read before the order
// is. "Cynthoni Femcels Forever" and "Femcels Forever EP" are three tokens each
// with neither inside the other, so width has nothing to say, and taking the
// later would take the one still carrying the "EP" that the catalogue ladder
// spends an entire rung shedding, peer folders being named without it. A phrase
// cannot be asked that -- both end in the same two letters, and in "Airwaves EP"
// they are the middle of a name -- but the ladder that built the rung can, and
// sources.Rung says so. So a rung carrying the label loses to one that is not,
// and only where that says nothing either does the ladder's order decide.
//
// One rung is what a repeat is for. It asks whether the peers who were quiet a
// moment ago are answering now, not whether a narrower spelling would have done
// better: the narrow spellings had their turn on the first walk and found
// nothing. Asking them again spends a bounded budget on the least likely half of
// the ladder, and spends the thing the budget does not measure -- re-asking an
// identical phrase is what earns this instance its thirty-minute Soulseek bans
// (issue #193), and one rung rather than six is seven searches for an empty want
// where two walks were twelve.
//
// A ladder with no phrase in it is not asked again; there is nothing to ask.
func broadestRung(rungs []sources.Rung) []sources.Rung {
	broadest, fewest := -1, 0
	for index, rung := range rungs {
		tokens := len(strings.Fields(rung.Text))
		if tokens == 0 {
			continue
		}
		if broadest < 0 || tokens < fewest {
			broadest, fewest = index, tokens
			continue
		}
		if tokens > fewest {
			continue
		}
		// The same width. The label the catalogue hangs off a title is the one
		// thing that tells two such rungs apart; where neither carries it, or both
		// do, the ladder's own order has the last word and the later rung wins.
		if rungs[broadest].CarriesReleaseKind || !rung.CarriesReleaseKind {
			broadest = index
		}
	}
	if broadest < 0 {
		return nil
	}
	return rungs[broadest : broadest+1]
}

// wait sleeps unless the caller gives up first, reporting whether it slept. A
// zero delay returns immediately, which is what tests use to skip the pause.
func wait(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// queryTracks carries the catalogue into the search, so a candidate can be
// corroborated against the release rather than only scored for quality.
func queryTracks(tracks []db.SourceSearchTrack) []sources.QueryTrack {
	result := make([]sources.QueryTrack, 0, len(tracks))
	for _, track := range tracks {
		result = append(result, sources.QueryTrack{
			Title: track.Title, DurationSeconds: track.DurationSeconds,
		})
	}
	return result
}

func storedCandidates(candidates []sources.Candidate) []db.SourceSearchCandidate {
	stored := make([]db.SourceSearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		files := make([]db.SourceSearchFile, 0, len(candidate.Files))
		for _, file := range candidate.Files {
			files = append(files, db.SourceSearchFile{
				Path: file.Path, Name: file.Name, Extension: file.Extension,
				SizeBytes: file.SizeBytes, BitRate: file.BitRate,
				DurationSeconds: file.DurationSeconds, VariableBitRate: file.VariableBitRate,
			})
		}
		stored = append(stored, db.SourceSearchCandidate{
			Provider: candidate.Provider, Username: candidate.Username,
			Directory: candidate.Directory, TotalSizeBytes: candidate.TotalSizeBytes,
			Format: candidate.Format, AverageBitRate: candidate.AverageBitRate,
			FreeUploadSlot: candidate.FreeUploadSlot, QueueLength: candidate.QueueLength,
			UploadSpeed: candidate.UploadSpeed, Score: candidate.Score,
			Match: db.SourceSearchMatch{
				Expected:  candidate.Match.Expected,
				Confirmed: candidate.Match.Confirmed,
				Partial:   candidate.Match.Partial,
				Absent:    candidate.Match.Absent,
			},
			Reasons: candidate.Reasons, Files: files,
		})
	}
	return stored
}

func storedRefusals(refused []sources.Refused) []db.SourceSearchRefusal {
	stored := make([]db.SourceSearchRefusal, 0, len(refused))
	for _, one := range refused {
		stored = append(stored, db.SourceSearchRefusal{
			Username: one.Username, Directory: one.Directory,
			Format: one.Format, Reason: one.Reason, Kind: one.Kind,
		})
	}
	return stored
}

// SearchFor looks for one thing, in the caller's own words, and reports what was
// offered and which phrase found it.
//
// It is the same search a release run makes — the same provider, the same
// bounded timeout, the same phrase ladder walked in the same order, and the same
// refusal to believe an empty result the first time — with no run and no release
// behind it. A want has neither, and searching for one through the run machinery
// would mean inventing a row for something nobody is reviewing.
//
// What it does differently is both sides of that refusal, because a want's budget
// is bounded and a release run's is not. That budget is derived from the ladder:
// every rung gets the provider's complete post-admission allowance, the repeat
// gets one more, the pause before the repeat is counted, and fixed headroom
// covers the work around all of it. Shared-gate waiting spends that headroom, and
// the provider checks the allowance again after admission so no rung starts
// unless it can still finish. The repeat asks the broadest rung alone rather than
// the ladder again, since that is the rung a second reply window is worth
// spending on and the whole ladder twice is not budgeted. A repeat still cut
// short -- by a caller whose own deadline is shorter, or by rungs that overran --
// reports the first walk's empty answer rather than a provider that never
// answered.
// The copies the user's stored preferences turned away are reported alongside
// what was offered, because they are a different answer from nothing. A want
// whose every offer was in a refused format has been told something with
// somebody to act on it; "nothing on offer" would not be true.
// The preferences that were applied come back with the answer. The caller
// chooses one file out of a folder, and the folder's bitrate is an average: the
// same floor has to be put to the file itself, and it can only be put by
// somebody holding it.
func (searcher *Searcher) SearchFor(
	ctx context.Context, query sources.Query, rungs []sources.Rung,
) ([]sources.Candidate, []sources.Refused, sources.Preferences, string, error) {
	return searcher.searchFor(ctx, query, rungs, false)
}

// SearchForWithFloorWaived searches with bitrate floors removed for one want.
// Format refusals remain active.
func (searcher *Searcher) SearchForWithFloorWaived(
	ctx context.Context, query sources.Query, rungs []sources.Rung,
) ([]sources.Candidate, []sources.Refused, sources.Preferences, string, error) {
	return searcher.searchFor(ctx, query, rungs, true)
}

// StoredPreferences reports the settings a waived search left out of its
// returned preferences. Acquisition uses them when it harvests siblings from
// the same folder.
func (searcher *Searcher) StoredPreferences(ctx context.Context) sources.Preferences {
	return searcher.preferences(ctx)
}

func (searcher *Searcher) searchFor(
	ctx context.Context, query sources.Query, rungs []sources.Rung, waiveFloor bool,
) ([]sources.Candidate, []sources.Refused, sources.Preferences, string, error) {
	preferences := searcher.preferences(ctx)
	if waiveFloor {
		preferences.MinimumBitRate = 0
		preferences.FormatMinimumBitRate = nil
	}
	provider, err := searcher.provider(ctx)
	if err != nil {
		return nil, nil, preferences, "", err
	}
	budget := searcher.searchBudget(len(rungs), provider.SearchBudget())
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	candidates, refused, text, exhausted, err := searcher.askWithinBudgetWithPreferences(
		ctx, provider, query, sources.QueryTexts(rungs), preferences)
	if err != nil {
		if outOfBudget(ctx, err) {
			return nil, refused, preferences, text, nil
		}
		return nil, refused, preferences, text, err
	}
	if exhausted {
		return nil, refused, preferences, text, nil
	}
	// Soulseek search is a broadcast that collects replies for a bounded window,
	// so nothing found means nobody answered in time rather than nobody has it.
	if len(candidates) == 0 {
		if !wait(ctx, searcher.emptyRetryDelay) {
			err := ctx.Err()
			if outOfBudget(ctx, err) {
				return nil, refused, preferences, text, nil
			}
			return nil, refused, preferences, text, err
		}
		again, againRefused, againText, exhausted, err := searcher.askWithinBudgetWithPreferences(
			ctx, provider, query, sources.QueryTexts(broadestRung(rungs)), preferences)
		// The repeat asks the same network in the same words, so the peers who
		// answer it are largely the ones who answered the first ask. Their
		// refusals replace what was already counted rather than adding to it: a
		// person told "six copies below your floor" about three copies asked
		// after twice has been given a number that means nothing.
		if len(againRefused) > 0 {
			refused = againRefused
		}
		if err != nil {
			if outOfBudget(ctx, err) {
				return nil, refused, preferences, text, nil
			}
			return nil, refused, preferences, againText, err
		}
		if exhausted {
			return nil, refused, preferences, text, nil
		}
		candidates, text = again, againText
	}
	return candidates, refused, preferences, text, nil
}

// outOfBudget reports whether a bounded search stopped because its allowance
// was exhausted, and for no other reason.
//
// That is worth telling apart, because a search whose budget ran out has an
// answer already: the first walk asked every phrase and heard nothing back,
// which is the thing the repeat exists to confirm rather than to produce.
// Losing it to "the provider never answered" is the worse account of the world,
// and it costs a want its turn over a confirmation there was no time left to
// hold (see searchBudget for what the time is spent on).
//
// A provider may say directly that shared admission left too little time to
// start; that signal means nothing was asked. For a deadline error, both halves
// of the test carry weight and neither is sufficient. The caller's state alone
// would swallow a refusal that merely arrived after the clock ran out -- slskd
// reporting the search abandoned is how a Soulseek ban is seen at all, and
// recording that as nobody sharing the music is issue #193 again. The error
// alone would swallow one rung's own reply window expiring while the caller
// still had budget, which is a provider that stopped answering rather than a
// search that ran out of road. Only a deadline error raised while the caller's
// deadline is the one that passed is exhaustion; cancellation still surfaces.
func outOfBudget(ctx context.Context, err error) bool {
	return errors.Is(err, sources.ErrSearchBudgetExhausted) ||
		(errors.Is(err, context.DeadlineExceeded) &&
			errors.Is(ctx.Err(), context.DeadlineExceeded))
}
