package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// The overview counts rows in the browser's zone. What is proved here is the
// shape the page relies on — thirty days ending today, fourteen, twelve
// months, a Monday-first week of hours — and that a listen lands in the local
// day and hour it was heard, not the UTC one.

type overviewJSON struct {
	Listening struct {
		Available bool `json:"available"`
		Listens   struct {
			Total    int64 `json:"total"`
			Previous int64 `json:"previous"`
			Days     []struct {
				Date  string `json:"date"`
				Count int64  `json:"count"`
			} `json:"days"`
		} `json:"listens"`
		MostPlayed []struct {
			Title     string  `json:"title"`
			Listens   int64   `json:"listens"`
			TrackID   *string `json:"trackId"`
			InLibrary bool    `json:"inLibrary"`
			CoverURL  *string `json:"coverUrl"`
		} `json:"mostPlayed"`
		TopArtists []struct {
			Name       string  `json:"name"`
			Listens    int64   `json:"listens"`
			PictureURL *string `json:"pictureUrl"`
		} `json:"topArtists"`
		WhenYouListen struct {
			Cells          [7][24]int64 `json:"cells"`
			PeakHour       int          `json:"peakHour"`
			BusiestWeekday string       `json:"busiestWeekday"`
		} `json:"whenYouListen"`
		TopAlbums []struct{ Title string } `json:"topAlbums"`
		Sessions  []struct {
			StartedAt time.Time `json:"startedAt"`
			EndedAt   time.Time `json:"endedAt"`
			Songs     int64     `json:"songs"`
			Artists   []string  `json:"artists"`
			Ongoing   bool      `json:"ongoing"`
			CoverURLs []string  `json:"coverUrls"`
		} `json:"sessions"`
	} `json:"listening"`
	Arrived struct {
		Today int64 `json:"today"`
		Days  []struct {
			Count int64 `json:"count"`
		} `json:"days"`
	} `json:"arrived"`
	Library struct {
		FileCount int64 `json:"fileCount"`
		Growth    []struct {
			Month string `json:"month"`
			Files int64  `json:"files"`
		} `json:"growth"`
	} `json:"library"`
	RecentlyAdded []struct {
		Title    string  `json:"title"`
		CoverURL *string `json:"coverUrl"`
	} `json:"recentlyAdded"`
}

func readOverview(t *testing.T, handler http.Handler, zone string) overviewJSON {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/overview?tz="+zone, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /overview = %d: %s", recorder.Code, recorder.Body.String())
	}
	var out overviewJSON
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	return out
}

func TestOverviewCountsListensInTheBrowsersZone(t *testing.T) {
	pool := dbtest.Setup(t)
	store := db.New(pool)
	handler := NewAPI(store, pool, &fakeArtistSearcher{}, zerolog.Nop(), WithOverview(store))
	ctx := context.Background()

	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Skip("no zone database on this machine")
	}
	now := time.Now().In(vienna)
	// A listen an hour ago, one thirty-five days ago (the "previous" window),
	// and two of one recording this month so it tops Most played. Times are
	// chosen away from midnight so the local day is the one the test computes.
	recent := now.Add(-time.Hour)
	before := now.Add(-35 * 24 * time.Hour)
	_, err = store.InsertListens(ctx, []db.ListenInsert{
		{ListenedAt: recent, ArtistName: "Pashanim", TrackName: "Maske weg", ReleaseName: "traence",
			RecordingMBID: "8bc0f5f1-d052-429b-9123-1040f7e2b4b6", ArtistMBIDs: []string{"33333333-3333-3333-3333-333333333333"}},
		{ListenedAt: recent.Add(-time.Minute), ArtistName: "Pashanim", TrackName: "Maske weg", ReleaseName: "traence",
			RecordingMBID: "8bc0f5f1-d052-429b-9123-1040f7e2b4b6"},
		{ListenedAt: recent.Add(-2 * time.Minute), ArtistName: "Playboi Carti", TrackName: "No Lie"},
		{ListenedAt: before, ArtistName: "Playboi Carti", TrackName: "Stop Breathing"},
	})
	if err != nil {
		t.Fatalf("InsertListens() error = %v", err)
	}
	// Pulling the same page again stores nothing twice.
	stored, err := store.InsertListens(ctx, []db.ListenInsert{
		{ListenedAt: recent, ArtistName: "Pashanim", TrackName: "Maske weg", ReleaseName: "traence"},
	})
	if err != nil || stored.Stored != 0 {
		t.Fatalf("second insert stored %d, err %v; want 0", stored.Stored, err)
	}

	// The catalogue holds Pashanim under the listen's artist ID, with a
	// picture, and Playboi Carti by name only, without one.
	var pashanimID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (name, sort_name, musicbrainz_id)
		VALUES ('Pashanim', 'pashanim', '33333333-3333-3333-3333-333333333333')
		RETURNING id::text`).Scan(&pashanimID); err != nil {
		t.Fatalf("insert an artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO artist_images (artist_id, image, content_type, source)
		VALUES ($1::uuid, '\x89504e47'::bytea, 'image/png', 'test')`, pashanimID); err != nil {
		t.Fatalf("insert an artist picture: %v", err)
	}

	got := readOverview(t, handler, "Europe/Vienna")
	if !got.Listening.Available {
		t.Fatal("listening should be available with listens stored")
	}
	if len(got.Listening.Listens.Days) != 30 {
		t.Fatalf("days = %d, want 30", len(got.Listening.Listens.Days))
	}
	last := got.Listening.Listens.Days[29]
	if last.Date != now.Format("2006-01-02") && last.Date != recent.Format("2006-01-02") {
		t.Errorf("last day = %s, want today in Vienna (%s)", last.Date, now.Format("2006-01-02"))
	}
	if got.Listening.Listens.Total != 3 {
		t.Errorf("total = %d, want the 3 listens of the last 30 days", got.Listening.Listens.Total)
	}
	if got.Listening.Listens.Previous != 1 {
		t.Errorf("previous = %d, want the 1 listen 35 days ago", got.Listening.Listens.Previous)
	}

	if len(got.Listening.MostPlayed) == 0 || got.Listening.MostPlayed[0].Title != "Maske weg" || got.Listening.MostPlayed[0].Listens != 2 {
		t.Errorf("most played = %+v, want Maske weg with 2", got.Listening.MostPlayed)
	}
	if got.Listening.MostPlayed[0].TrackID != nil || got.Listening.MostPlayed[0].InLibrary {
		t.Errorf("a recording the catalogue does not hold has no track: %+v", got.Listening.MostPlayed[0])
	}
	if len(got.Listening.TopArtists) == 0 || got.Listening.TopArtists[0].Name != "Pashanim" {
		t.Errorf("top artists = %+v", got.Listening.TopArtists)
	}
	if picture := got.Listening.TopArtists[0].PictureURL; picture == nil || *picture != "/api/v1/artists/"+pashanimID+"/image" {
		t.Errorf("Pashanim picture = %v, want the catalogue artist's image", picture)
	}
	for _, artist := range got.Listening.TopArtists[1:] {
		if artist.PictureURL != nil {
			t.Errorf("%s has no catalogue picture, got %s", artist.Name, *artist.PictureURL)
		}
	}

	cells := got.Listening.WhenYouListen.Cells
	day := (int(recent.Weekday()) + 6) % 7 // Monday first
	if cells[day][recent.Hour()] < 1 {
		t.Errorf("the listen at %s should land in cell [%d][%d]; cells = %v", recent, day, recent.Hour(), cells)
	}
	if got.Listening.WhenYouListen.BusiestWeekday != weekdays[day] {
		t.Errorf("busiest weekday = %s, want %s", got.Listening.WhenYouListen.BusiestWeekday, weekdays[day])
	}
	if len(got.Listening.TopAlbums) != 1 || got.Listening.TopAlbums[0].Title != "traence" {
		t.Errorf("top albums = %+v", got.Listening.TopAlbums)
	}
	// Three listens two minutes apart are one sitting; the one 35 days ago is
	// outside the window.
	if len(got.Listening.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one", got.Listening.Sessions)
	}
	session := got.Listening.Sessions[0]
	if session.Songs != 3 || len(session.Artists) != 2 || session.Artists[0] != "Playboi Carti" || session.Artists[1] != "Pashanim" {
		t.Errorf("session = %+v, want 3 songs, Playboi Carti first because it was heard first", session)
	}
	if !session.EndedAt.After(session.StartedAt) || session.EndedAt.Sub(session.StartedAt) != 2*time.Minute {
		t.Errorf("session runs %s to %s, want two minutes", session.StartedAt, session.EndedAt)
	}
	// Its last listen was an hour ago, so the next listen would start a new
	// session: not ongoing.
	if session.Ongoing {
		t.Error("a session whose last listen is an hour old is not ongoing")
	}
}

func TestOverviewShapesTheLibraryFiguresWithoutListens(t *testing.T) {
	pool := dbtest.Setup(t)
	store := db.New(pool)
	handler := NewAPI(store, pool, &fakeArtistSearcher{}, zerolog.Nop(), WithOverview(store))
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (path, size_bytes, modified_at, title_tag, artist_tag)
		VALUES ('/music/a.flac', 1000, now(), 'Nachtfalter', 'Nemo Vice')`); err != nil {
		t.Fatalf("insert a file: %v", err)
	}

	got := readOverview(t, handler, "Not/AZone")
	if got.Listening.Available {
		t.Error("no listens stored, so listening must not be available")
	}
	if len(got.Listening.Listens.Days) != 30 || got.Listening.Listens.Total != 0 {
		t.Errorf("listens with nothing stored = %+v", got.Listening.Listens)
	}
	if len(got.Arrived.Days) != 14 || got.Arrived.Today != 1 {
		t.Errorf("arrived = %+v, want 14 days with the file that joined today", got.Arrived)
	}
	if got.Library.FileCount != 1 {
		t.Errorf("file count = %d, want 1", got.Library.FileCount)
	}
	if len(got.Library.Growth) != 12 {
		t.Fatalf("growth months = %d, want 12", len(got.Library.Growth))
	}
	thisMonth := time.Now().UTC().Format("2006-01")
	if last := got.Library.Growth[11]; last.Month != thisMonth || last.Files != 1 {
		t.Errorf("last growth month = %+v, want %s with 1 file", last, thisMonth)
	}
	if len(got.RecentlyAdded) != 1 || got.RecentlyAdded[0].Title != "Nachtfalter" {
		t.Errorf("recently added = %+v", got.RecentlyAdded)
	}
}

// A listen the player scrobbled without MusicBrainz IDs, and a file the
// scanner has not tied to any track, both used to show no picture. The file
// whose tags equal the listen's names is the one that was played, and a file
// with no release still carries its own picture, so both rows point at it.
func TestOverviewPicturesRowsFromTheHeldFile(t *testing.T) {
	pool := dbtest.Setup(t)
	store := db.New(pool)
	handler := NewAPI(store, pool, &fakeArtistSearcher{}, zerolog.Nop(), WithOverview(store))
	ctx := context.Background()

	var fileID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (path, size_bytes, modified_at, title_tag, artist_tag)
		VALUES ('/music/no-lie.flac', 1000, now(), 'No Lie', 'playboi carti')
		RETURNING id::text`).Scan(&fileID); err != nil {
		t.Fatalf("insert a file: %v", err)
	}
	if _, err := store.InsertListens(ctx, []db.ListenInsert{
		{ListenedAt: time.Now().Add(-time.Hour), ArtistName: "Playboi Carti", TrackName: "No Lie"},
		{ListenedAt: time.Now().Add(-2 * time.Hour), ArtistName: "Playboi Carti", TrackName: "Stop Breathing"},
	}); err != nil {
		t.Fatalf("InsertListens() error = %v", err)
	}

	got := readOverview(t, handler, "UTC")
	want := "/api/v1/library/files/" + fileID + "/cover"
	if len(got.Listening.MostPlayed) != 2 {
		t.Fatalf("most played = %+v, want two rows", got.Listening.MostPlayed)
	}
	for _, row := range got.Listening.MostPlayed {
		switch row.Title {
		case "No Lie":
			if row.CoverURL == nil || *row.CoverURL != want {
				t.Errorf("most played No Lie cover = %v, want %s", row.CoverURL, want)
			}
			if row.TrackID != nil || row.InLibrary {
				t.Errorf("a file found by its tags must not say the recording is held: %+v", row)
			}
		case "Stop Breathing":
			if row.CoverURL != nil {
				t.Errorf("Stop Breathing has no copy and no IDs, so no cover; got %s", *row.CoverURL)
			}
		}
	}
	// An hour of silence between the two listens makes two sessions. Neither
	// is still going, the newer one is pictured by the held file, the older
	// by nothing.
	sessions := got.Listening.Sessions
	if len(sessions) != 2 || sessions[0].Songs != 1 || sessions[1].Songs != 1 {
		t.Fatalf("sessions = %+v, want two of one song", sessions)
	}
	if sessions[0].Ongoing || sessions[1].Ongoing {
		t.Errorf("an hour-old listen is not an ongoing session: %+v", sessions)
	}
	if len(sessions[0].CoverURLs) != 1 || sessions[0].CoverURLs[0] != want {
		t.Errorf("newer session covers = %v, want [%s]", sessions[0].CoverURLs, want)
	}
	if len(sessions[1].CoverURLs) != 0 {
		t.Errorf("older session covers = %v, want none", sessions[1].CoverURLs)
	}
	if len(got.RecentlyAdded) != 1 || got.RecentlyAdded[0].CoverURL == nil || *got.RecentlyAdded[0].CoverURL != want {
		t.Errorf("recently added = %+v, want the file's own cover", got.RecentlyAdded)
	}
}
