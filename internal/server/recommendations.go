package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/recommendations"
)

// Recommendations is the reading side of the recommendation feature: the list a
// background sweep already stored, the hard suppression rules applied to it now,
// and the two decisions a reader can make about a row.
//
// It is separate from RecommendationSource, which asks ListenBrainz whether the
// account is reachable, because these answers come from the database alone. The
// list is readable with the network down, and the sweep is the only thing that
// talks to ListenBrainz.
type Recommendations interface {
	// Evaluate returns every stored candidate with the rule that suppresses it,
	// empty when there is none. The handler needs the suppressed rows to say how
	// many were hidden and why.
	Evaluate(ctx context.Context, source string) ([]recommendations.EvaluatedCandidate, error)
	Dismiss(
		ctx context.Context, subject recommendations.DismissalSubject, musicBrainzID uuid.UUID,
	) (db.RecommendationDismissal, error)
	RecordFeedback(
		ctx context.Context, recordingID uuid.UUID, signal recommendations.FeedbackSignal,
	) (db.RecommendationFeedback, error)
	ClearFeedback(context.Context) error
	// RecordImpressions counts a showing for each recording the stored list
	// actually offers, and answers how many that was. Recordings it does not
	// offer are dropped rather than refused: a reader can report a row that a
	// sweep replaced while they were looking at it, and that is ordinary.
	RecordImpressions(ctx context.Context, musicBrainzRecordingIDs []uuid.UUID) (int, error)
}

// RecommendationSnapshotStore reads the header of the stored answer: which
// source it came from, whether the last sweep finished, and when. It is read
// separately from the candidates because a list with no rows has two very
// different meanings — nothing has been fetched yet, or everything fetched is
// suppressed — and only the header tells them apart.
//
// It also reads the state of the background sweep itself, from the job queue,
// because the header alone cannot say whether the list is still being kept up to
// date. A sweep that cannot reach ListenBrainz leaves the previous list where it
// is, correctly, and the header of that list still says the sweep which built it
// finished. Only the queue says a later sweep tried and got nowhere.
type RecommendationSnapshotStore interface {
	RecommendationSnapshot(ctx context.Context, source string) (db.RecommendationSnapshot, error)
	RecommendationSweepState(ctx context.Context) (db.RecommendationSweepState, error)
}

// WithRecommendations registers what reads the stored recommendation list and
// records a reader's decisions about it. Optional: without it the routes report
// themselves unavailable and everything else on the installation is unchanged.
func WithRecommendations(service Recommendations) Option {
	return func(api *API) {
		api.recommendationList = service
	}
}

// defaultRecommendationSource is the only source there is today. It is a
// parameter rather than a constant in the query because the stores are keyed by
// source, and a second one would otherwise mean changing every caller.
const defaultRecommendationSource = "listenbrainz"

// recommendationResponse is one suggestion, as a reader sees it. Every
// identifier is a MusicBrainz identifier, because that is what the dismissal
// routes take back.
type recommendationResponse struct {
	RecordingID    uuid.UUID   `json:"recordingId"`
	ReleaseGroupID uuid.UUID   `json:"releaseGroupId"`
	ArtistIDs      []uuid.UUID `json:"artistIds"`
	RecordingTitle string      `json:"recordingTitle"`
	ReleaseTitle   string      `json:"releaseTitle"`
	ArtistName     string      `json:"artistName"`
	Rank           int32       `json:"rank"`
	// ReasonCodes are the source's own words for why it offered this recording.
	// They are carried through untouched: nothing here decides what they mean.
	ReasonCodes []string `json:"reasonCodes"`
}

// recommendationSnapshotResponse says where the list came from and when, so a
// reader can tell a list nobody has fetched yet from one fetched an hour ago.
type recommendationSnapshotResponse struct {
	Source string `json:"source"`
	// Fetched is false when no sweep has stored anything for this source.
	Fetched   bool       `json:"fetched"`
	Status    string     `json:"status"`
	Detail    string     `json:"detail"`
	FetchedAt *time.Time `json:"fetchedAt"`
}

// recommendationRefreshResponse says whether the list is still being refreshed,
// and when the next attempt to refresh it is due.
//
// A background sweep rebuilds the list once a day and nobody presses anything.
// When it stops being able to do that — ListenBrainz is down, or the account is
// larger than one sweep may walk — the last complete list stays on the screen and
// quietly gets older. Nothing said so, and a list from last week looked exactly
// like a list from this morning (issue #316).
type recommendationRefreshResponse struct {
	// Attempted is false when no sweep has ever run to an end here. A stored list
	// with no sweep behind it is ordinary — a seeded or restored database — and
	// must never read as a fault.
	Attempted bool `json:"attempted"`
	// Succeeded says the last attempt left the list refreshed. A sweep still at
	// work counts as succeeding so far: it walks a large account over many short
	// passes, only the last of which publishes, and a chain that has not finished
	// has not failed.
	Succeeded bool `json:"succeeded"`
	// LastAttemptAt is when the last pass ended, and NextAttemptAt is when the
	// waiting pass may run. NextAttemptAt is null when no pass is waiting, which
	// is itself worth saying: the list is then not going to be refreshed at all.
	LastAttemptAt *time.Time `json:"lastAttemptAt"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
}

// recommendationHiddenResponse counts what the six product entries removed, over the
// whole stored list rather than the page being read. A reader is owed the
// number and the reason: a page of three suggestions out of forty is otherwise
// indistinguishable from a source that had little to say.
type recommendationHiddenResponse struct {
	Total             int `json:"total"`
	Owned             int `json:"owned"`
	Requested         int `json:"requested"`
	NotWanted         int `json:"notWanted"`
	FollowedArtist    int `json:"followedArtist"`
	FollowedLabel     int `json:"followedLabel"`
	Dismissed         int `json:"dismissed"`
	Unkept            int `json:"unkept"`
	ImpressionFatigue int `json:"impressionFatigue"`
}

const (
	defaultRecommendationLimit = 50
	maximumRecommendationLimit = 200
)

// listRecommendations returns the suggestions that may be shown now.
//
// Suppression is applied on every read, so a follow, a request, a dismissal, a
// new file in the library or an expired fatigue window takes effect without
// waiting for the next sweep. Suppressed rows never reach the response; only
// their count and the rule that removed them do.
//
// That is also why a page is asked for by the rank of a row and not only by a
// position. Rank is the stored order the sweep wrote, and it does not move. A
// position in the visible list does move, because reading a page can suppress
// the rows on it — three showings with no answer is one of the hard rules — and
// every row after them then shifts up by one while the reader's position stays
// where it was. Stepping by position skipped a page of suggestions in silence
// (issue #329). `after` and `before` name a rank the reader was just shown, so
// a row leaving mid-walk removes one row instead of moving all of them.
//
// `offset` remains for the two ends of the list, which are exact however much
// the rules have removed: the first page starts at the start, and the last page
// is the last page of whatever is left.
func (api *API) listRecommendations(response http.ResponseWriter, request *http.Request) {
	if api.recommendationList == nil {
		api.problem(response, http.StatusServiceUnavailable, "recommendations are unavailable", nil)
		return
	}
	limit, offset := defaultRecommendationLimit, 0
	var after, before *int32
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximumRecommendationLimit {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"limit must be between 1 and " + strconv.Itoa(maximumRecommendationLimit)})
			return
		}
		limit = value
	}
	asked := 0
	if raw := strings.TrimSpace(request.URL.Query().Get("offset")); raw != "" {
		asked++
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"offset must be zero or greater"})
			return
		}
		offset = value
	}
	for name, target := range map[string]**int32{"after": &after, "before": &before} {
		raw := strings.TrimSpace(request.URL.Query().Get(name))
		if raw == "" {
			continue
		}
		asked++
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{name + " must be the rank of a suggestion"})
			return
		}
		rank := int32(value)
		*target = &rank
	}
	// Two of them name two different pages, and there is no way to tell which
	// one the reader pressed. Answering one of them silently would be a page
	// nobody asked for.
	if asked > 1 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"name only one of offset, after and before"})
		return
	}

	evaluated, err := api.recommendationList.Evaluate(request.Context(), defaultRecommendationSource)
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	hidden := recommendationHiddenResponse{}
	visible := make([]recommendationResponse, 0, len(evaluated))
	for _, candidate := range evaluated {
		switch candidate.SuppressedBy {
		case "":
			visible = append(visible, recommendationResponseFrom(candidate))
			continue
		case recommendations.SuppressedOwned:
			hidden.Owned++
		case recommendations.SuppressedRequested:
			hidden.Requested++
		case recommendations.SuppressedNotWanted:
			hidden.NotWanted++
		case recommendations.SuppressedFollowedArtist:
			hidden.FollowedArtist++
		case recommendations.SuppressedFollowedLabel:
			hidden.FollowedLabel++
		case recommendations.SuppressedDismissed:
			hidden.Dismissed++
		case recommendations.SuppressedUnkept:
			hidden.Unkept++
		case recommendations.SuppressedImpressionFatigue:
			hidden.ImpressionFatigue++
		}
		hidden.Total++
	}

	total := len(visible)
	offset = recommendationWindow(visible, limit, offset, after, before)
	end := offset + limit
	if end > total {
		end = total
	}

	snapshot := api.recommendationSnapshot(request.Context(), defaultRecommendationSource)
	api.writeJSON(response, http.StatusOK, map[string]any{
		"items":    visible[offset:end],
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"hidden":   hidden,
		"snapshot": snapshot,
		"refresh":  api.recommendationRefresh(request.Context(), snapshot),
	})
}

// recommendationWindow says where the page a reader asked for starts, counted
// in the list they may see now.
//
// `after` and `before` name the rank of a row they were just shown. The page
// after that row is the visible rows ranked below it; the page before it is the
// last page-full of the visible rows ranked above it. Rank is the order the
// sweep stored and it does not move, so neither answer depends on what the hard
// rules removed between one press and the next.
//
// A window that has run off the end is moved back onto the list. That happens
// when the reader was on the last page and every row on it left — pressing "Not
// interested" on the last suggestion is the ordinary way — and an empty panel
// under a list that still has rows would be a dead end.
func recommendationWindow(
	visible []recommendationResponse, limit, offset int, after, before *int32,
) int {
	total := len(visible)
	start := offset
	switch {
	case after != nil:
		start = total
		for index, item := range visible {
			if item.Rank > *after {
				start = index
				break
			}
		}
	case before != nil:
		preceding := 0
		for _, item := range visible {
			if item.Rank >= *before {
				break
			}
			preceding++
		}
		start = preceding - limit
	}
	if start >= total {
		start = ((total - 1) / limit) * limit
	}
	if start < 0 || total == 0 {
		start = 0
	}
	return start
}

// recommendationSnapshot reads the stored header, and answers "nothing fetched"
// for a source no sweep has stored. A header that cannot be read is reported the
// same way rather than failing the list: the suggestions themselves are the
// answer, and provenance beside them is a courtesy.
func (api *API) recommendationSnapshot(
	ctx context.Context, source string,
) recommendationSnapshotResponse {
	result := recommendationSnapshotResponse{Source: source}
	if api.recommendationSnapshots == nil {
		return result
	}
	row, err := api.recommendationSnapshots.RecommendationSnapshot(ctx, source)
	if errors.Is(err, pgx.ErrNoRows) {
		return result
	}
	if err != nil {
		api.logger.Error().Err(err).Str("source", source).Msg("read recommendation snapshot")
		return result
	}
	result.Fetched = true
	result.Status = row.Status
	result.Detail = row.Detail
	result.FetchedAt = nullableTime(row.FetchedAt.Valid, row.FetchedAt.Time)
	return result
}

// recommendationRefresh reads the job queue and says whether the sweep is still
// refreshing the stored list.
//
// The sweep records nothing about an attempt that changed nothing, and it does
// not have to: a pass that reached nobody keeps the previous list and finishes
// tidily, so the two facts to compare are when the last pass began and when the
// stored list was written. A pass writes the list it publishes before the queue
// marks the pass done, so a pass that refreshed the list left it written at or
// after that pass started. An older list means the pass published nothing.
//
// A chain still at work is not a fault. One sweep walks a large account over
// many short passes and only the last of them publishes, so every pass but the
// last leaves a list older than itself. Such a pass queues its successor to run
// at once, which is what a claimed pass, or a waiting pass whose wait is already
// over, means here.
//
// A queue that cannot be read is reported as nothing to say, in the same way the
// header is: the suggestions are the answer, and how fresh they are is said
// beside them.
func (api *API) recommendationRefresh(
	ctx context.Context, snapshot recommendationSnapshotResponse,
) recommendationRefreshResponse {
	result := recommendationRefreshResponse{}
	if api.recommendationSnapshots == nil {
		return result
	}
	state, err := api.recommendationSnapshots.RecommendationSweepState(ctx)
	if err != nil {
		api.logger.Error().Err(err).Msg("read recommendation sweep state")
		return result
	}
	if !state.Attempted {
		return result
	}
	result.Attempted = true
	result.LastAttemptAt = nullableTime(state.Finished.Valid, state.Finished.Time)
	result.NextAttemptAt = nullableTime(state.NextRunAfter.Valid, state.NextRunAfter.Time)

	refreshed := snapshot.FetchedAt != nil && state.Started.Valid &&
		!snapshot.FetchedAt.Before(state.Started.Time)
	atWork := state.Running ||
		(state.NextRunAfter.Valid && !state.NextRunAfter.Time.After(time.Now()))
	result.Succeeded = refreshed || atWork
	return result
}

func recommendationResponseFrom(candidate recommendations.EvaluatedCandidate) recommendationResponse {
	artistIDs := candidate.MusicBrainzArtistIDs
	if artistIDs == nil {
		artistIDs = []uuid.UUID{}
	}
	reasonCodes := candidate.ReasonCodes
	if reasonCodes == nil {
		reasonCodes = []string{}
	}
	return recommendationResponse{
		RecordingID:    candidate.MusicBrainzRecordingID,
		ReleaseGroupID: candidate.MusicBrainzReleaseGroupID,
		ArtistIDs:      artistIDs,
		RecordingTitle: candidate.RecordingTitle,
		ReleaseTitle:   candidate.ReleaseTitle,
		ArtistName:     candidate.ArtistName,
		Rank:           candidate.Rank,
		ReasonCodes:    reasonCodes,
	}
}

// dismissRecommendationRequest names what the reader said no to. The subject is
// the level the decision applies at, and it is permanent at that level: a
// dismissed artist stays dismissed however the source words the next suggestion.
type dismissRecommendationRequest struct {
	Subject       string `json:"subject"`
	MusicBrainzID string `json:"musicBrainzId"`
}

var recommendationSubjects = map[string]recommendations.DismissalSubject{
	"recording":     recommendations.DismissRecording,
	"release_group": recommendations.DismissRelease,
	"artist":        recommendations.DismissArtist,
}

// dismissRecommendation records that the reader is not interested.
//
// It writes to the recommendation dismissal store and nowhere else. This is a
// statement about taste, and it is kept apart from every store that holds a
// statement about audio identity — a review-queue rejection says a file is not
// the recording it was fetched for, which is not an opinion about the music
// (ADR 0005).
func (api *API) dismissRecommendation(response http.ResponseWriter, request *http.Request) {
	if api.recommendationList == nil {
		api.problem(response, http.StatusServiceUnavailable, "recommendations are unavailable", nil)
		return
	}
	var input dismissRecommendationRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}

	problems := make([]string, 0, 2)
	subject, known := recommendationSubjects[strings.TrimSpace(input.Subject)]
	if !known {
		problems = append(problems, "subject must be recording, release_group or artist")
	}
	musicBrainzID, err := uuid.Parse(strings.TrimSpace(input.MusicBrainzID))
	if err != nil {
		problems = append(problems, "musicBrainzId must be a UUID")
	}
	if len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	row, err := api.recommendationList.Dismiss(request.Context(), subject, musicBrainzID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"subject":       row.SubjectType,
		"musicBrainzId": row.MusicbrainzID,
		"dismissedAt":   nullableTime(row.DismissedAt.Valid, row.DismissedAt.Time),
	})
}

type recommendationFeedbackRequest struct {
	RecordingID string `json:"recordingId"`
	Signal      string `json:"signal"`
}

// recordRecommendationFeedback records a soft opinion against the current
// candidate. The service copies the candidate context, so the client cannot
// submit a stale or invented neighbourhood.
func (api *API) recordRecommendationFeedback(response http.ResponseWriter, request *http.Request) {
	if api.recommendationList == nil {
		api.problem(response, http.StatusServiceUnavailable, "recommendations are unavailable", nil)
		return
	}
	var input recommendationFeedbackRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	recordingID, err := uuid.Parse(strings.TrimSpace(input.RecordingID))
	if err != nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"recordingId must be a UUID"})
		return
	}
	var signal recommendations.FeedbackSignal
	switch strings.TrimSpace(input.Signal) {
	case string(recommendations.FeedbackMoreLikeThis):
		signal = recommendations.FeedbackMoreLikeThis
	case string(recommendations.FeedbackLessLikeThis):
		signal = recommendations.FeedbackLessLikeThis
	default:
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"signal must be more_like_this or less_like_this"})
		return
	}
	row, err := api.recommendationList.RecordFeedback(request.Context(), recordingID, signal)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusConflict, "suggestion is stale", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"recordingId": row.MusicbrainzRecordingID,
		"signal":      row.Signal,
		"createdAt":   nullableTime(row.CreatedAt.Valid, row.CreatedAt.Time),
	})
}

// clearRecommendationFeedback removes the current source's soft signals and
// leaves dismissals, impressions, wants and review decisions untouched.
func (api *API) clearRecommendationFeedback(response http.ResponseWriter, request *http.Request) {
	if api.recommendationList == nil {
		api.problem(response, http.StatusServiceUnavailable, "recommendations are unavailable", nil)
		return
	}
	if err := api.recommendationList.ClearFeedback(request.Context()); err != nil {
		api.internalError(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// recordRecommendationImpressionsRequest carries the recordings the interface
// has just put in front of somebody, in one call because a list is shown in one
// go.
type recordRecommendationImpressionsRequest struct {
	RecordingIDs []string `json:"recordingIds"`
}

// recordRecommendationImpressions counts a showing.
//
// The fifth suppression rule is "shown and ignored": after three showings with
// no decision, a recording is held back for ninety days. The count therefore has
// to be written by the thing that did the showing, which is the interface, and
// not by the read that fetched the list — a list fetched by a page nobody
// looked at was not shown to anybody. Repeated calls inside a day count once,
// so a reader who reloads is not punished for it.
//
// Only a recording the stored list offers may be counted. The route takes any
// UUID a caller cares to send, and without that check the fatigue count could
// be written about music this installation was never going to suggest
// (issue #327).
func (api *API) recordRecommendationImpressions(response http.ResponseWriter, request *http.Request) {
	if api.recommendationList == nil {
		api.problem(response, http.StatusServiceUnavailable, "recommendations are unavailable", nil)
		return
	}
	var input recordRecommendationImpressionsRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if len(input.RecordingIDs) == 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"recordingIds must name at least one recording"})
		return
	}
	if len(input.RecordingIDs) > maximumRecommendationLimit {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"recordingIds must name " + strconv.Itoa(maximumRecommendationLimit) + " recordings or fewer"})
		return
	}

	recordingIDs := make([]uuid.UUID, 0, len(input.RecordingIDs))
	for _, raw := range input.RecordingIDs {
		parsed, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"every recordingId must be a UUID"})
			return
		}
		recordingIDs = append(recordingIDs, parsed)
	}

	recorded, err := api.recommendationList.RecordImpressions(request.Context(), recordingIDs)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"recorded": recorded})
}
