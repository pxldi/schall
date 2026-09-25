package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
)

// OwnSource is the source name the own engine stores its snapshot and reads
// its feedback under (ADR 0039 §4).
const OwnSource = "schall"

const (
	// ownSessionGap ends a listening session: a longer gap between two listens
	// starts a new one (ADR 0039 §2).
	ownSessionGap = 30 * time.Minute
	// ownMinimumPlays keeps single listens out of the pool, because one listen
	// can be a radio or autoplay pick nobody chose (ADR 0039 §1).
	ownMinimumPlays = 2
	// ownMaximumSeeds bounds seed_recording_ids, the key ADR 0028's feedback
	// neighbourhood reads (ADR 0039 §3).
	ownMaximumSeeds = 5
	// The two score weights are starting values in taste-space, which ADR 0028
	// permits tuning. They order the list and never decide what is hidden.
	ownSharedSessionsWeight = 0.7
	ownPlaysWeight          = 0.3
)

// listenedRecordingsKey names, in reason_context, every pool recording
// MusicBrainz answered about under the stored row's identifier. The next pass
// finds the stored expansion through each of them and does not ask again.
const listenedRecordingsKey = "listened_recording_ids"

// OwnSweepPosition is what one pass of an own sweep hands the next.
//
// Passes counts the chain. Unexpandable is every pool recording MusicBrainz
// answered about and could not expand, so no pass of the chain asks about it
// twice. A new chain starts with none and asks once more.
type OwnSweepPosition struct {
	Passes       int         `json:"passes,omitempty"`
	Unexpandable []uuid.UUID `json:"unexpandable,omitempty"`
}

// OwnSweepResult tells the worker whether the chain goes on. More means the
// next pass runs at once. Resume means the chain is unfinished and the next
// job carries Position.
type OwnSweepResult struct {
	More     bool
	Resume   bool
	Position OwnSweepPosition
	Report   OwnSweepReport
}

// OwnSweepReport is one pass in numbers. Pool is how many recordings were
// played at least twice and are not owned. Cached is how many kept the
// expansion the published snapshot held, and Asked is how many MusicBrainz
// answered about in this pass. ProviderFailed says MusicBrainz stopped
// answering part way.
type OwnSweepReport struct {
	Pool           int
	Cached         int
	Asked          int
	Stored         int
	Status         string
	Detail         string
	ProviderFailed bool
	Written        bool
}

// ownCandidate is one pool recording with its source score.
type ownCandidate struct {
	recordingID      uuid.UUID
	plays            int64
	latestListenedAt time.Time
	sharedSessions   int64
	seedIDs          []uuid.UUID
	score            float64
}

// SweepOwnPage runs one pass of the own engine (ADR 0039).
//
// It reads the pool, keeps every expansion the published `schall` snapshot
// already holds, asks MusicBrainz about at most maximumCandidateExpansions
// others in rank order, and writes the whole snapshot. It never reads the
// ListenBrainz account: the listens a sync already copied are its only input.
func (service *Service) SweepOwnPage(ctx context.Context, position OwnSweepPosition) (OwnSweepResult, error) {
	if service.recordings == nil {
		return OwnSweepResult{}, errors.New("MusicBrainz recording expansion is not configured")
	}
	rows, err := service.store.OwnRecommendationPool(ctx, db.OwnRecommendationPoolParams{
		SessionGapMicroseconds: ownSessionGap.Microseconds(),
		MinimumPlays:           ownMinimumPlays,
		MaximumSeeds:           ownMaximumSeeds,
	})
	if err != nil {
		return OwnSweepResult{}, fmt.Errorf("read the own recommendation pool: %w", err)
	}
	pool := rankOwnPool(rows)

	published, err := service.store.RecommendationCandidates(ctx, OwnSource)
	if err != nil {
		return OwnSweepResult{}, fmt.Errorf("read the published own recommendations: %w", err)
	}
	cache, err := publishedExpansions(published)
	if err != nil {
		return OwnSweepResult{}, err
	}

	unexpandable := make(map[uuid.UUID]struct{}, len(position.Unexpandable))
	for _, recordingID := range position.Unexpandable {
		unexpandable[recordingID] = struct{}{}
	}
	passes := position.Passes + 1

	expanded := make(map[uuid.UUID]recordingExpansion, len(pool))
	pending := make([]uuid.UUID, 0, len(pool))
	for _, candidate := range pool {
		if recording, held := cache[candidate.recordingID]; held {
			expanded[candidate.recordingID] = recording
			continue
		}
		if _, known := unexpandable[candidate.recordingID]; known {
			continue
		}
		pending = append(pending, candidate.recordingID)
	}
	cached := len(expanded)

	// Rank order is the source score. Feedback moves rows only when the
	// snapshot is ranked, so it never decides what is looked up (ADR 0028).
	asking := pending
	if len(asking) > maximumCandidateExpansions {
		asking = asking[:maximumCandidateExpansions]
	}
	answered, failure := 0, ""
	for _, recordingID := range asking {
		recording, usable, err := service.expandRecording(ctx, recordingID)
		if err != nil {
			failure = fmt.Sprintf("MusicBrainz expansion stopped at %s: %v", recordingID, err)
			break
		}
		answered++
		if !usable {
			unexpandable[recordingID] = struct{}{}
			continue
		}
		expanded[recordingID] = recording
	}

	left := 0
	for _, recordingID := range pending {
		_, done := expanded[recordingID]
		_, refused := unexpandable[recordingID]
		if !done && !refused {
			left++
		}
	}
	lookedUp := len(pool) - left

	inputs := ownInputs(pool, expanded)
	unfinished := left > 0
	spent := unfinished && passes >= maximumSweepPasses
	reached := failure == "" || answered > 0

	report := OwnSweepReport{
		Pool:           len(pool),
		Cached:         cached,
		Asked:          answered,
		ProviderFailed: failure != "",
	}
	next := OwnSweepPosition{Passes: passes, Unexpandable: carriedUnexpandable(pool, unexpandable)}
	result := OwnSweepResult{
		More:     unfinished && !spent && reached,
		Resume:   unfinished && !spent,
		Position: next,
	}

	status, detail := "complete", ""
	switch {
	case spent:
		status, detail = "partial", fmt.Sprintf(
			"the sweep stopped after %d passes with %d of %d recordings looked up in MusicBrainz",
			passes, lookedUp, len(pool))
	case unfinished:
		status, detail = "partial", fmt.Sprintf(
			"%d of %d recordings looked up in MusicBrainz", lookedUp, len(pool))
	}
	if failure != "" {
		detail = strings.Join([]string{detail, failure}, "; ")
	}
	report.Status, report.Detail = status, detail

	// A pass with nothing to store and records still waiting is not an answer,
	// so it writes nothing and whatever is published stays.
	if len(inputs) == 0 && unfinished {
		result.Report = report
		return result, nil
	}

	// An empty pool is written as an empty complete snapshot. ListenBrainz
	// answering nothing can be an outage (issue #317), but the pool is read
	// from local tables and an empty one is the answer.
	feedback, err := service.store.ListRecommendationFeedback(ctx, OwnSource)
	if err != nil {
		return OwnSweepResult{}, fmt.Errorf("read own recommendation feedback: %w", err)
	}
	if err := service.replaceSnapshot(ctx, Snapshot{
		Source:     OwnSource,
		Status:     status,
		Detail:     detail,
		Candidates: inputs,
	}, feedback); err != nil {
		return OwnSweepResult{}, err
	}
	report.Stored = len(inputs)
	report.Written = true
	result.Report = report
	return result, nil
}

// rankOwnPool scores the pool (ADR 0039 §2) and orders it by score, ties by
// MBID. A maximum of zero contributes zero.
func rankOwnPool(rows []db.OwnRecommendationPoolRow) []ownCandidate {
	var mostShared, mostPlays int64
	for _, row := range rows {
		mostShared = max(mostShared, row.SharedSessions)
		mostPlays = max(mostPlays, row.Plays)
	}
	pool := make([]ownCandidate, 0, len(rows))
	for _, row := range rows {
		if row.RecordingMbid == uuid.Nil {
			continue
		}
		pool = append(pool, ownCandidate{
			recordingID:      row.RecordingMbid,
			plays:            row.Plays,
			latestListenedAt: row.LatestListenedAt.Time,
			sharedSessions:   row.SharedSessions,
			seedIDs:          row.SeedRecordingIds,
			score:            ownScore(row.SharedSessions, mostShared, row.Plays, mostPlays),
		})
	}
	sort.Slice(pool, func(left, right int) bool {
		if pool[left].score != pool[right].score {
			return pool[left].score > pool[right].score
		}
		return pool[left].recordingID.String() < pool[right].recordingID.String()
	})
	return pool
}

func ownScore(shared, mostShared, plays, mostPlays int64) float64 {
	score := 0.0
	if mostShared > 0 {
		score += ownSharedSessionsWeight * float64(shared) / float64(mostShared)
	}
	if mostPlays > 0 {
		score += ownPlaysWeight * float64(plays) / float64(mostPlays)
	}
	return score
}

// publishedExpansions reads the published snapshot as a cache of MusicBrainz
// answers, keyed by the recording each row is stored under and by every
// merged recording the listens carried for it.
func publishedExpansions(rows []db.RecommendationCandidate) (map[uuid.UUID]recordingExpansion, error) {
	cache := make(map[uuid.UUID]recordingExpansion, len(rows))
	for _, row := range rows {
		recording := recordingExpansion{
			recordingID:    row.MusicbrainzRecordingID,
			releaseGroupID: row.MusicbrainzReleaseGroupID,
			artistIDs:      append([]uuid.UUID(nil), row.MusicbrainzArtistIds...),
			title:          row.RecordingTitle,
			releaseTitle:   row.ReleaseTitle,
			artistName:     row.ArtistName,
		}
		cache[row.MusicbrainzRecordingID] = recording
		var context map[string]json.RawMessage
		if len(row.ReasonContext) > 0 {
			if err := json.Unmarshal(row.ReasonContext, &context); err != nil {
				return nil, fmt.Errorf("decode own recommendation %s context: %w", row.MusicbrainzRecordingID, err)
			}
		}
		var listened []string
		if raw, ok := context[listenedRecordingsKey]; ok && json.Unmarshal(raw, &listened) == nil {
			for _, value := range listened {
				if listenedID, err := uuid.Parse(value); err == nil {
					cache[listenedID] = recording
				}
			}
		}
	}
	return cache, nil
}

// ownInputs builds the snapshot rows for every pool recording with an
// expansion. Two pool recordings MusicBrainz merged into one stored recording
// would conflict, so the higher-ranked one gives the row its counts. Every
// merged pool recording is named on the row, so none is asked about again.
func ownInputs(pool []ownCandidate, expanded map[uuid.UUID]recordingExpansion) []CandidateInput {
	aliases := make(map[uuid.UUID][]string, len(expanded))
	for _, candidate := range pool {
		recording, ok := expanded[candidate.recordingID]
		if ok && recording.recordingID != candidate.recordingID {
			aliases[recording.recordingID] = append(aliases[recording.recordingID], candidate.recordingID.String())
		}
	}

	inputs := make([]CandidateInput, 0, len(expanded))
	stored := make(map[uuid.UUID]struct{}, len(expanded))
	for _, candidate := range pool {
		recording, ok := expanded[candidate.recordingID]
		if !ok {
			continue
		}
		if _, taken := stored[recording.recordingID]; taken {
			continue
		}
		stored[recording.recordingID] = struct{}{}

		reasons := []string{"listened"}
		context := map[string]any{"plays": candidate.plays}
		if !candidate.latestListenedAt.IsZero() {
			context["latest_listened_at"] = candidate.latestListenedAt.UTC().Format(time.RFC3339)
		}
		if candidate.sharedSessions > 0 {
			reasons = append(reasons, "co_listened")
			context["shared_sessions"] = candidate.sharedSessions
			seeds := make([]string, 0, len(candidate.seedIDs))
			for _, seedID := range candidate.seedIDs {
				seeds = append(seeds, seedID.String())
			}
			context["seed_recording_ids"] = seeds
		}
		if merged := aliases[recording.recordingID]; len(merged) > 0 {
			sort.Strings(merged)
			context[listenedRecordingsKey] = merged
		}
		score := candidate.score
		inputs = append(inputs, recording.input(&score, reasons, context))
	}
	return inputs
}

// carriedUnexpandable keeps the refused recordings still in the pool, sorted so
// the same chain writes the same payload.
func carriedUnexpandable(pool []ownCandidate, unexpandable map[uuid.UUID]struct{}) []uuid.UUID {
	carried := make([]uuid.UUID, 0, len(unexpandable))
	for _, candidate := range pool {
		if _, refused := unexpandable[candidate.recordingID]; refused {
			carried = append(carried, candidate.recordingID)
		}
	}
	sort.Slice(carried, func(left, right int) bool {
		return carried[left].String() < carried[right].String()
	})
	return carried
}
