package identity

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// pagedProvider answers a search by name with one page for the title alone and
// another for the title on an album, the way MusicBrainz does for a song with
// hundreds of live rows.
type pagedProvider struct {
	byName    []musicbrainz.Recording
	onAlbum   []musicbrainz.Recording
	searches  []musicbrainz.RecordingQuery
	lastLimit int
}

func (provider *pagedProvider) Recording(context.Context, uuid.UUID) (musicbrainz.Recording, error) {
	return musicbrainz.Recording{}, musicbrainz.ErrNotFound
}

func (provider *pagedProvider) ResolveRecordingID(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	return id, nil
}

func (provider *pagedProvider) SearchRecordings(
	_ context.Context, query musicbrainz.RecordingQuery, limit int,
) ([]musicbrainz.Recording, error) {
	provider.searches = append(provider.searches, query)
	provider.lastLimit = limit
	if query.Release != "" {
		return provider.onAlbum, nil
	}
	if len(provider.byName) > limit {
		return provider.byName[:limit], nil
	}
	return provider.byName, nil
}

const (
	studioLithium  = "3fac712c-4196-4865-8c1a-94231a7da309"
	bootlegLithium = "aeced029-3a2b-4d29-b9c3-89eb2067e5e6"
)

// liveLithiums is a full page of Nirvana's "Lithium" as MusicBrainz ranks it:
// bootlegs only, one of them within five seconds of the studio file.
func liveLithiums() []musicbrainz.Recording {
	rows := []musicbrainz.Recording{
		recording(bootlegLithium, "Lithium", "Nirvana", 261000, "1993-11-18: Unplugged"),
	}
	// The other bootlegs run well outside the tolerance, so the one above is the
	// only row on the page that fits.
	for index := 1; len(rows) < nameSearchLimit; index++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", index)
		rows = append(rows, recording(id, "Lithium", "Nirvana", 200000+index*100,
			fmt.Sprintf("Live bootleg %d", index)))
	}
	return rows
}

// A studio file used to land on a live bootleg: the search returned a page of
// bootlegs, one of them ran four seconds off, and nothing else on the page fit.
// A full page cannot rule out the rows it left out, so the file is a question.
func TestAFullPageOfLiveRowsDoesNotProveTheOneThatFits(t *testing.T) {
	provider := &pagedProvider{byName: liveLithiums()}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Subject: SubjectFile, Artist: "Nirvana", Title: "Lithium", DurationMS: 257000,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusNeedsReview {
		t.Fatalf("status = %q (%s), want the file held for a person", outcome.Status, outcome.Summary)
	}
	if provider.lastLimit != nameSearchLimit {
		t.Errorf("limit = %d, want a full page of %d", provider.lastLimit, nameSearchLimit)
	}
}

// The album tag reaches the studio row. Every row on "Nevermind" was read, so a
// row left unread is on another album, and the album picks the one on it.
func TestTheAlbumPicksTheStudioRowOutOfAFullPage(t *testing.T) {
	provider := &pagedProvider{
		byName: liveLithiums(),
		onAlbum: []musicbrainz.Recording{
			recording(studioLithium, "Lithium", "Nirvana", 257040, "Nevermind"),
			recording(bootlegLithium, "Lithium", "Nirvana", 261000, "1993-11-18: Unplugged"),
		},
	}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Subject: SubjectFile, Artist: "Nirvana", Title: "Lithium", Album: "Nevermind",
		DurationMS: 257000,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusResolved {
		t.Fatalf("status = %q (%s), want the studio row", outcome.Status, outcome.Summary)
	}
	if got := outcome.Identity.RecordingID.String(); got != studioLithium {
		t.Errorf("recording = %s, want the studio row %s", got, studioLithium)
	}
	if outcome.Identity.Method != "title-artist-duration-and-release" {
		t.Errorf("method = %q", outcome.Identity.Method)
	}
	if len(provider.searches) != 2 || provider.searches[1].Release != "Nevermind" {
		t.Errorf("searches = %+v, want the title, then the title on the album", provider.searches)
	}
}

// Two rows on the album fit as well as each other, the way "Nevermind" carries a
// live "Lithium" on its deluxe edition. The album cannot choose, so a person does.
func TestTwoRowsOnTheAlbumStayAQuestion(t *testing.T) {
	provider := &pagedProvider{
		byName: liveLithiums(),
		onAlbum: []musicbrainz.Recording{
			recording(studioLithium, "Lithium", "Nirvana", 257040, "Nevermind"),
			recording(secondID, "Lithium", "Nirvana", 256000, "Nevermind"),
		},
	}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Subject: SubjectFile, Artist: "Nirvana", Title: "Lithium", Album: "Nevermind",
		DurationMS: 257000,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusNeedsReview {
		t.Fatalf("status = %q (%s), want a question", outcome.Status, outcome.Summary)
	}
}

// The album search came back full too, so rows on "Nevermind" may be unread and
// the album cannot vouch for the one it found.
func TestAFullAlbumPageDoesNotSingleOutItsRow(t *testing.T) {
	onAlbum := []musicbrainz.Recording{
		recording(studioLithium, "Lithium", "Nirvana", 257040, "Nevermind"),
	}
	for index := 1; len(onAlbum) < nameSearchLimit; index++ {
		id := fmt.Sprintf("00000000-0000-4000-9000-%012d", index)
		onAlbum = append(onAlbum, recording(id, "Lithium", "Nirvana", 180000, "Nevermind"))
	}
	provider := &pagedProvider{byName: liveLithiums(), onAlbum: onAlbum}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Subject: SubjectFile, Artist: "Nirvana", Title: "Lithium", Album: "Nevermind",
		DurationMS: 257000,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusNeedsReview {
		t.Fatalf("status = %q (%s), want a question", outcome.Status, outcome.Summary)
	}
}

// A page short of full is every row there is, and a song with one row that fits
// is identified as before, with one search.
func TestAShortPageStillProvesTheOneRowThatFits(t *testing.T) {
	provider := &pagedProvider{byName: []musicbrainz.Recording{
		recording(studioLithium, "Lithium", "Nirvana", 257040, "Nevermind"),
		recording(bootlegLithium, "Lithium", "Nirvana", 290000, "1993-11-18: Unplugged"),
	}}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Subject: SubjectFile, Artist: "Nirvana", Title: "Lithium", Album: "Nevermind",
		DurationMS: 257000,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusResolved || outcome.Identity.RecordingID.String() != studioLithium {
		t.Fatalf("outcome = %q (%s), want the studio row", outcome.Status, outcome.Summary)
	}
	if len(provider.searches) != 1 {
		t.Errorf("searches = %d, want one", len(provider.searches))
	}
}
