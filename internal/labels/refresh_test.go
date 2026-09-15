package labels

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

type fakeLabelRefreshStore struct {
	label       db.LabelForRefreshRow
	linked      []uuid.UUID
	advancedTo  int32
	completed   bool
	queuedNext  int
	labelForErr error
}

func (store *fakeLabelRefreshStore) LabelForRefresh(context.Context, uuid.UUID) (db.LabelForRefreshRow, error) {
	return store.label, store.labelForErr
}

func (store *fakeLabelRefreshStore) LinkLabelRelease(_ context.Context, params db.LinkLabelReleaseParams) error {
	store.linked = append(store.linked, params.AlbumID)
	return nil
}

func (store *fakeLabelRefreshStore) AdvanceLabelRefresh(_ context.Context, params db.AdvanceLabelRefreshParams) error {
	store.advancedTo = params.ReleasesOffset
	return nil
}

func (store *fakeLabelRefreshStore) CompleteLabelRefresh(context.Context, uuid.UUID) error {
	store.completed = true
	return nil
}

func (store *fakeLabelRefreshStore) QueueLabelRefresh(context.Context, uuid.UUID) (db.QueueLabelRefreshRow, error) {
	store.queuedNext++
	return db.QueueLabelRefreshRow{}, nil
}

type fakeLabelProvider struct {
	page  musicbrainz.LabelReleasePage
	err   error
	asked int
}

func (provider *fakeLabelProvider) LabelReleases(
	context.Context, uuid.UUID, int, int,
) (musicbrainz.LabelReleasePage, error) {
	provider.asked++
	return provider.page, provider.err
}

type fakeLabelCatalogue struct{}

func (fakeLabelCatalogue) SaveCatalogueReleaseGroup(
	context.Context, musicbrainz.ReleaseGroupDetail,
) (uuid.UUID, error) {
	return uuid.New(), nil
}

// An unfollow can land between one pass queuing the next and that pass
// running. A walk that did not check would keep reading a label nobody asked
// about any more, and keep requeueing itself forever.
func TestRefreshStopsWithoutAskingWhenTheLabelHasBeenUnfollowed(t *testing.T) {
	labelID := uuid.New()
	store := &fakeLabelRefreshStore{label: db.LabelForRefreshRow{
		ID: labelID, MusicbrainzID: uuid.New(), Name: "Bloodfire Descent",
		ReleasesOffset: 100,
		// Unfollowed: the row survives, the walk does not resume into it.
		FollowedAt: pgtype.Timestamptz{Valid: false},
	}}
	provider := &fakeLabelProvider{page: musicbrainz.LabelReleasePage{Total: 500, Read: 100}}
	refresher := New(store, provider, fakeLabelCatalogue{}, zerolog.Nop())

	more, err := refresher.Refresh(context.Background(), labelID)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if more {
		t.Fatalf("more = true, want false: an unfollowed label has nothing left to walk")
	}
	if provider.asked != 0 {
		t.Fatalf("MusicBrainz was asked %d times about an unfollowed label, want 0", provider.asked)
	}
	if store.completed {
		t.Fatalf("the walk was marked complete; an unfollow is not a finished walk")
	}
}

// The ordinary case: a followed label with more releases than one pass reads
// asks MusicBrainz and reports more waiting.
func TestRefreshAsksMusicBrainzForAFollowedLabel(t *testing.T) {
	labelID := uuid.New()
	store := &fakeLabelRefreshStore{label: db.LabelForRefreshRow{
		ID: labelID, MusicbrainzID: uuid.New(), Name: "Bloodfire Descent",
		FollowedAt: pgtype.Timestamptz{Valid: true},
	}}
	provider := &fakeLabelProvider{page: musicbrainz.LabelReleasePage{Total: 500, Read: 100}}
	refresher := New(store, provider, fakeLabelCatalogue{}, zerolog.Nop())

	more, err := refresher.Refresh(context.Background(), labelID)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if !more {
		t.Fatalf("more = false, want true: 100 of 500 releases read")
	}
	if provider.asked == 0 {
		t.Fatalf("MusicBrainz was never asked, want at least one page read")
	}
}
