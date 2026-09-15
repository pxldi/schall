// Package activity reads what Schall has been doing, as a story rather than as
// machinery.
//
// Everything here already happened and was already written down somewhere else:
// a copy that arrived, one that was refused and why, one nothing could decide,
// a file that was deleted and under whose licence. Nothing in this package
// records anything of its own. That is deliberate — a table of its own would
// oblige every writer that exists now and every one written later to remember
// to fill it in, and the one thing tonight's schema work showed is that a new
// place must not have to remember.
//
// It is also not the jobs view. That one says which worker ran what, which is
// the right answer to "is anything stuck" and the wrong one to "what happened
// while I was away".
package activity

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kind is what sort of thing happened. The vocabulary is short on purpose: a
// feed with a name for every event is a log, and a log is what the reader is
// trying not to read.
type Kind string

const (
	// Arrived is a copy that was proven and taken into the library.
	Arrived Kind = "arrived"
	// Refused is a copy that was fetched and turned out not to be the
	// recording, by its audio or by its own tags.
	Refused Kind = "refused"
	// Asked is a copy nothing could decide, kept as a question for a person.
	Asked Kind = "asked"
	// Removed is a file that left the disc, under a recorded licence.
	Removed Kind = "removed"
)

// Event is one thing that happened, or a run of the same thing.
//
// Count is why a run is one row. On the evening this was written the library
// re-encoded five hundred and forty-three files in twenty-three minutes; five
// hundred and forty-three rows saying the same sentence is not a story anybody
// reads, and it would have buried the four events of the day worth seeing. A
// run of one is an ordinary event and says nothing about its count.
type Event struct {
	Kind Kind `json:"kind"`
	// At is when it happened, and for a run the most recent of them.
	At time.Time `json:"at"`
	// Since is when a run started. Equal to At for a single event.
	Since time.Time `json:"since"`
	// Count is how many of this ran together. One for a single event.
	Count int `json:"count"`
	// Title and Artist name the music, where the record named it. A run names
	// the most recent one; the count says how many others there were.
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	// Detail is the sentence the record already carried — the verdict's own
	// summary, or the licence's reason. Never written here, only passed on.
	Detail string `json:"detail,omitempty"`
	// Href is the page this can be acted on, empty where there is nothing to
	// act on. A row that leads nowhere is a row that reports rather than asks.
	Href string `json:"href,omitempty"`
}

// Window is how far back a feed reads, and Limit how many rows it may return
// after runs have been collapsed.
const (
	Window = 24 * time.Hour
	Limit  = 40
)

// Reader answers what has been happening.
type Reader struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

// feedQuery is one read over the records that already exist.
//
// The verdicts come from acquisition_target_files, which carries a sentence
// written for a person in `summary`; the removals from library_file_removals,
// which carries the licence's own `reason` and who asked. Neither is
// paraphrased here. A row that named no music is still an event — it says a
// file went — so nothing is dropped for having an empty title.
const feedQuery = `
SELECT kind, at, title, artist, detail, target_id
FROM (
    SELECT
        CASE files.verdict
            WHEN 'accepted' THEN 'arrived'
            WHEN 'held' THEN 'asked'
            ELSE 'refused'
        END AS kind,
        files.decided_at AS at,
        coalesce(targets.entry_title, '') AS title,
        coalesce(targets.entry_artist, '') AS artist,
        files.summary AS detail,
        targets.id AS target_id
    FROM acquisition_target_files files
    JOIN acquisition_targets targets ON targets.id = files.acquisition_target_id
    WHERE files.decided_at >= $1
      AND files.verdict IN ('accepted', 'held', 'discarded_audio', 'discarded_tags')

    UNION ALL

    SELECT
        'removed' AS kind,
        removals.removed_at AS at,
        -- The file's own name, which is the only thing a licence keeps that a
        -- person recognises: the library row is gone by the time the licence is
        -- written, so there is nothing left to join to for a title. A row
        -- reading "Deleted · A file" told the reader nothing at all.
        regexp_replace(removals.library_path, '^.*/', '') AS title,
        '' AS artist,
        removals.reason AS detail,
        NULL::uuid AS target_id
    FROM library_file_removals removals
    WHERE removals.removed_at >= $1
) events
ORDER BY at DESC
LIMIT $2
`

// readLimit is how many raw rows one read may draw before they are collapsed.
// A burst of several hundred re-encodes is one line once collapsed, so the
// window has to be read wide enough that a burst cannot push everything else
// out of it.
const readLimit = 2000

// Since reads what happened after a moment, newest first, with runs of the same
// kind collapsed into one row each.
func (reader *Reader) Since(ctx context.Context, since time.Time) ([]Event, error) {
	rows, err := reader.pool.Query(ctx, feedQuery, since, readLimit)
	if err != nil {
		return nil, fmt.Errorf("read what has been happening: %w", err)
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		var event Event
		var kind string
		var targetID uuid.NullUUID
		if err := rows.Scan(&kind, &event.At, &event.Title, &event.Artist,
			&event.Detail, &targetID); err != nil {
			return nil, fmt.Errorf("read one thing that happened: %w", err)
		}
		event.Kind = Kind(kind)
		event.Since = event.At
		event.Count = 1
		event.Href = href(event.Kind, targetID)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read what has been happening: %w", err)
	}
	return collapse(events), nil
}

// href is the page a row can be acted on. Only a question has one: an arrival
// and a refusal are settled, and a link that opens a page saying "nothing to
// decide" is a link that wasted the press.
func href(kind Kind, targetID uuid.NullUUID) string {
	if kind == Asked && targetID.Valid {
		return "/review?want=" + targetID.UUID.String()
	}
	return ""
}

// collapse folds neighbouring events of one kind into a single row.
//
// Neighbouring, not all of a kind: the point is to keep the order things
// happened in. Two arrivals with a refusal between them stay three rows,
// because that is what the evening actually looked like. It is only the run —
// the sweep that did the same thing four hundred times without stopping — that
// becomes one line.
func collapse(events []Event) []Event {
	collapsed := make([]Event, 0, len(events))
	for _, event := range events {
		last := len(collapsed) - 1
		if last >= 0 && collapsed[last].Kind == event.Kind {
			collapsed[last].Count++
			// Read newest first, so the event arriving is the older one and it
			// is the run's beginning.
			collapsed[last].Since = event.At
			continue
		}
		collapsed = append(collapsed, event)
		if len(collapsed) == Limit {
			break
		}
	}
	return collapsed
}
