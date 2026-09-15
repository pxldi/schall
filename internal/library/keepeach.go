package library

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
)

// SweptDuplicates is what one press of "keep one of each" came to.
type SweptDuplicates struct {
	// Resolved is the recordings left holding one copy, and RemovedPaths every
	// file that went, so the screen can name them rather than say a number.
	Resolved     int      `json:"resolved"`
	RemovedPaths []string `json:"removedPaths"`
	// LeftAlone is the recordings this would not choose for, and Why says of
	// each one what stopped it. They are the whole point of the answer: a
	// person who presses this and is told "23 resolved" when four were skipped
	// has been told the library is tidy when it is not.
	LeftAlone int      `json:"leftAlone"`
	Why       []string `json:"why"`
	// StillOnDisc is a file whose library row went and whose audio would not
	// delete. The music is out of the library either way.
	StillOnDisc []string `json:"stillOnDisc"`
}

// KeepOneOfEach keeps one copy of every recording the library holds more than
// once, and deletes the rest.
//
// It is one caller standing in for many, and it is not a new judgement. Each
// recording is settled exactly as pressing "keep this one" on its own row would
// settle it: where one copy is provably the better one, that copy stays; where
// the copies are the same audio and nothing separates them, one is kept because
// the caller has said they do not mind which.
//
// What it will not do is choose where Schall does not know it is looking at the
// same audio twice. A length that differs by more than the matching slack, a
// file nothing has decoded, two lossy codecs that cannot be compared, a copy
// somebody matched by hand — those are left exactly as they were and named in
// the answer. "They are all fine" is a thing a person can truthfully say about
// copies; it is not something they can say about files Schall cannot tell are
// copies. removedBy names the actor written to each removal licence.
func (remover *Remover) KeepOneOfEach(ctx context.Context, removedBy string) (SweptDuplicates, error) {
	swept := SweptDuplicates{RemovedPaths: []string{}, Why: []string{}, StillOnDisc: []string{}}

	held, err := db.New(remover.pool).RecordingsHeldTwice(ctx, false, db.MaxHeldTwiceRecordings, 0)
	if err != nil {
		return swept, fmt.Errorf("read the recordings held more than once: %w", err)
	}

	for _, recording := range held.Recordings {
		keeperID, chosen := db.ChooseKeeperWhenAnyWillDo(recording.Copies)
		if !chosen {
			swept.LeftAlone++
			swept.Why = append(swept.Why, fmt.Sprintf("%s — %s: %s",
				recording.Title, recording.Artist, recording.Verdict))
			continue
		}

		deleting := make([]uuid.UUID, 0, len(recording.Copies)-1)
		for _, copied := range recording.Copies {
			if copied.ID != keeperID {
				deleting = append(deleting, copied.ID)
			}
		}
		kept, err := remover.KeepOne(ctx, recording.RecordingID, keeperID, deleting, removedBy)
		swept.RemovedPaths = append(swept.RemovedPaths, kept.RemovedPaths...)
		swept.StillOnDisc = append(swept.StillOnDisc, kept.StillOnDisc...)
		if err != nil {
			// A recording that would not settle is reported and the pass carries
			// on. Stopping at the first would leave the rest of a press somebody
			// made undone, with nothing said about which part ran.
			swept.LeftAlone++
			swept.Why = append(swept.Why, fmt.Sprintf("%s — %s: %s",
				recording.Title, recording.Artist, err))
			continue
		}
		swept.Resolved++
	}
	return swept, nil
}
