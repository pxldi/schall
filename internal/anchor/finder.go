package anchor

import (
	"context"
	"errors"
)

// Upload is one video a name search returned.
//
// It is what the search says about the video and nothing more: no relevance
// rank travels with it, because the rank answers to the query and the choice
// has to answer to the song (docs/decisions/0029). Seconds is the video's
// length, which is the key the want is matched on. Topic status takes priority,
// and Views decides between videos of the same kind.
type Upload struct {
	ID      string
	Title   string
	Channel string
	Seconds int
	Views   int64
}

// Finder searches a video service by name and takes an excerpt out of one of
// its uploads.
//
// It is the one boundary in this package that is not keyed. Everything the
// distributor path does is fetched by ISRC; this is a text search, and the whole
// of what keeps it honest is the selection rule in choose.go and the rule in
// internal/identity that an anchor found this way may never refuse a copy.
type Finder interface {
	Available() bool
	Search(ctx context.Context, query string, limit int) ([]Upload, error)
	// Excerpt returns thirty seconds from the middle of the upload, as MP3 bytes.
	Excerpt(ctx context.Context, id string, durationSeconds int) ([]byte, error)
}

// What a Finder says when it cannot answer. None of them is a fact about the
// music: each one leaves the want on the retry ladder rather than recorded as
// having no anchor.
var (
	ErrFinderUnavailable = errors.New("no upload search is configured")
	ErrFinderRateLimited = errors.New("the upload search is refusing requests")
	ErrNoUpload          = errors.New("the upload search returned nothing")
)
