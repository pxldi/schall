package server

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/library"
)

// OverviewReader reads the figures the Overview page draws.
type OverviewReader interface {
	ValidTimeZone(context.Context, string) bool
	Overview(context.Context, string, time.Time) (db.Overview, error)
}

// WithOverview registers the store the Overview page reads.
func WithOverview(reader OverviewReader) Option {
	return func(api *API) {
		api.overviews = reader
	}
}

type overviewDay struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

type overviewPlayed struct {
	Title         string  `json:"title"`
	Artist        string  `json:"artist"`
	Listens       int64   `json:"listens"`
	RecordingMBID *string `json:"recordingMbid"`
	CoverURL      *string `json:"coverUrl"`
	TrackID       *string `json:"trackId"`
	InLibrary     bool    `json:"inLibrary"`
	Wanted        bool    `json:"wanted"`
}

type overviewArtist struct {
	Name       string  `json:"name"`
	ArtistMBID *string `json:"artistMbid"`
	Listens    int64   `json:"listens"`
	PictureURL *string `json:"pictureUrl"`
}

type overviewAlbum struct {
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Listens     int64   `json:"listens"`
	ReleaseMBID *string `json:"releaseMbid"`
	CoverURL    *string `json:"coverUrl"`
}

type overviewSession struct {
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Ongoing   bool      `json:"ongoing"`
	Songs     int64     `json:"songs"`
	Artists   []string  `json:"artists"`
	CoverURLs []string  `json:"coverUrls"`
}

type overviewHeat struct {
	Cells          [7][24]int64 `json:"cells"`
	PeakHour       int          `json:"peakHour"`
	BusiestWeekday string       `json:"busiestWeekday"`
}

type overviewListening struct {
	Available bool `json:"available"`
	Listens   struct {
		Total    int64         `json:"total"`
		Previous int64         `json:"previous"`
		Days     []overviewDay `json:"days"`
	} `json:"listens"`
	MostPlayed    []overviewPlayed  `json:"mostPlayed"`
	TopArtists    []overviewArtist  `json:"topArtists"`
	WhenYouListen overviewHeat      `json:"whenYouListen"`
	TopAlbums     []overviewAlbum   `json:"topAlbums"`
	Sessions      []overviewSession `json:"sessions"`
}

type overviewStorage struct {
	UsedBytes  int64 `json:"usedBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

type overviewMonth struct {
	Month string `json:"month"`
	Files int64  `json:"files"`
}

type overviewAdded struct {
	Title    string    `json:"title"`
	Artist   string    `json:"artist"`
	AddedAt  time.Time `json:"addedAt"`
	TrackID  *string   `json:"trackId"`
	CoverURL *string   `json:"coverUrl"`
}

type overviewResponse struct {
	Listening overviewListening `json:"listening"`
	Arrived   struct {
		Today       int64         `json:"today"`
		Downloading int64         `json:"downloading"`
		Days        []overviewDay `json:"days"`
	} `json:"arrived"`
	Library struct {
		FileCount  int64            `json:"fileCount"`
		TotalBytes int64            `json:"totalBytes"`
		Growth     []overviewMonth  `json:"growth"`
		Storage    *overviewStorage `json:"storage"`
	} `json:"library"`
	RecentlyAdded []overviewAdded `json:"recentlyAdded"`
}

var weekdays = [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

// overview answers GET /overview?tz=. Every figure is counted in the zone the
// browser sent; a zone Postgres does not know falls back to UTC.
func (api *API) overview(response http.ResponseWriter, request *http.Request) {
	if api.overviews == nil {
		api.problem(response, http.StatusNotFound, "The overview is not available on this server.", nil)
		return
	}
	ctx := request.Context()
	zone := request.URL.Query().Get("tz")
	if !api.overviews.ValidTimeZone(ctx, zone) {
		zone = "UTC"
	}
	data, err := api.overviews.Overview(ctx, zone, time.Now())
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	var out overviewResponse
	out.Listening.Available = data.ListenCount > 0
	out.Listening.Listens.Total = data.ListensTotal
	out.Listening.Listens.Previous = data.ListensBefore
	out.Listening.Listens.Days = days(data.ListenDays)
	out.Listening.MostPlayed = make([]overviewPlayed, 0, len(data.MostPlayed))
	for _, row := range data.MostPlayed {
		out.Listening.MostPlayed = append(out.Listening.MostPlayed, overviewPlayed{
			Title:         row.Title,
			Artist:        row.Artist,
			Listens:       row.Listens,
			RecordingMBID: text(row.RecordingMBID),
			CoverURL:      listenCoverURL(row.AlbumID, row.HasCover, row.FileID, row.CAAReleaseMBID, row.ReleaseMBID),
			TrackID:       uuidString(row.TrackID),
			InLibrary:     row.InLibrary,
			Wanted:        row.Wanted,
		})
	}
	out.Listening.TopArtists = make([]overviewArtist, 0, len(data.TopArtists))
	for _, row := range data.TopArtists {
		var picture *string
		if row.ArtistID.Valid {
			s := "/api/v1/artists/" + uuidFromPg(row.ArtistID).String() + "/image"
			picture = &s
		}
		out.Listening.TopArtists = append(out.Listening.TopArtists, overviewArtist{
			Name: row.Name, ArtistMBID: text(row.ArtistMBID), Listens: row.Listens, PictureURL: picture,
		})
	}
	out.Listening.WhenYouListen = heat(data.Heat)
	out.Listening.TopAlbums = make([]overviewAlbum, 0, len(data.TopAlbums))
	for _, row := range data.TopAlbums {
		out.Listening.TopAlbums = append(out.Listening.TopAlbums, overviewAlbum{
			Title: row.Title, Artist: row.Artist, Listens: row.Listens,
			ReleaseMBID: text(row.ReleaseMBID),
			CoverURL:    coverArchiveURL(row.CAAReleaseMBID, row.ReleaseMBID),
		})
	}
	out.Listening.Sessions = make([]overviewSession, 0, len(data.Sessions))
	for _, row := range data.Sessions {
		covers := make([]string, 0, len(row.Covers))
		for _, cover := range row.Covers {
			if url := listenCoverURL(cover.AlbumID, cover.HasCover, cover.FileID, cover.CAAReleaseMBID, cover.ReleaseMBID); url != nil {
				covers = append(covers, *url)
			}
		}
		out.Listening.Sessions = append(out.Listening.Sessions, overviewSession{
			StartedAt: row.StartedAt, EndedAt: row.EndedAt, Ongoing: row.Ongoing,
			Songs: row.Songs, Artists: row.Artists, CoverURLs: covers,
		})
	}

	out.Arrived.Days = days(data.ArrivedDays)
	if n := len(out.Arrived.Days); n > 0 {
		out.Arrived.Today = out.Arrived.Days[n-1].Count
	}
	out.Arrived.Downloading = data.Downloading

	out.Library.FileCount = data.FileCount
	out.Library.TotalBytes = data.TotalBytes
	out.Library.Growth = make([]overviewMonth, 0, len(data.Growth))
	for _, row := range data.Growth {
		out.Library.Growth = append(out.Library.Growth, overviewMonth{Month: row.Month, Files: row.Files})
	}
	out.Library.Storage = api.overviewStorage(ctx)

	out.RecentlyAdded = make([]overviewAdded, 0, len(data.RecentlyAdded))
	for _, row := range data.RecentlyAdded {
		out.RecentlyAdded = append(out.RecentlyAdded, overviewAdded{
			Title: row.Title, Artist: row.Artist, AddedAt: row.AddedAt,
			TrackID:  uuidString(row.TrackID),
			CoverURL: listenCoverURL(row.AlbumID, row.HasCover, row.FileID, pgtype.Text{}, pgtype.Text{}),
		})
	}
	api.writeJSON(response, http.StatusOK, out)
}

// overviewStorage measures the disc the first enabled music folder is on.
// Null when there is no folder or it cannot be read; a number nothing measured
// would be planned around.
func (api *API) overviewStorage(ctx context.Context) *overviewStorage {
	if api.library == nil {
		return nil
	}
	roots, err := api.library.ListLibraryRoots(ctx)
	if err != nil {
		return nil
	}
	for _, root := range roots {
		if !root.Enabled {
			continue
		}
		for _, volume := range library.Volumes([]string{root.Path}) {
			if volume.Measured {
				return &overviewStorage{
					UsedBytes:  volume.TotalBytes - volume.FreeBytes,
					TotalBytes: volume.TotalBytes,
				}
			}
		}
		return nil
	}
	return nil
}

func days(rows []db.OverviewDay) []overviewDay {
	out := make([]overviewDay, 0, len(rows))
	for _, row := range rows {
		out = append(out, overviewDay{Date: row.Date, Count: row.Count})
	}
	return out
}

// heat names the busiest hour over the week and the busiest weekday. Ties go
// to the earlier hour and the earlier day, so the answer is stable.
func heat(cells [7][24]int64) overviewHeat {
	out := overviewHeat{Cells: cells, BusiestWeekday: weekdays[0]}
	var byHour [24]int64
	var byDay [7]int64
	for day := range cells {
		for hour := range cells[day] {
			byHour[hour] += cells[day][hour]
			byDay[day] += cells[day][hour]
		}
	}
	for hour := range byHour {
		if byHour[hour] > byHour[out.PeakHour] {
			out.PeakHour = hour
		}
	}
	busiest := 0
	for day := range byDay {
		if byDay[day] > byDay[busiest] {
			busiest = day
		}
	}
	out.BusiestWeekday = weekdays[busiest]
	return out
}

func uuidFromPg(value pgtype.UUID) uuid.UUID {
	return uuid.UUID(value.Bytes)
}

func text(value pgtype.Text) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

func uuidString(value pgtype.UUID) *string {
	if !value.Valid {
		return nil
	}
	s := uuidFromPg(value).String()
	return &s
}

func albumCoverURL(albumID pgtype.UUID) *string {
	s := "/api/v1/albums/" + uuidFromPg(albumID).String() + "/cover"
	return &s
}

func fileCoverURL(fileID pgtype.UUID) *string {
	s := "/api/v1/library/files/" + uuidFromPg(fileID).String() + "/cover"
	return &s
}

// listenCoverURL picks the picture for a row that names a track: a cover
// already cached for its release, else the archive's picture of the release
// the listen named, else the release's cover fetched on request (the album
// endpoint asks the archives on a miss and remembers the answer), else the
// picture packed into the held file itself. A row with none of these shows
// no picture.
func listenCoverURL(albumID pgtype.UUID, hasCover bool, fileID pgtype.UUID, caa, release pgtype.Text) *string {
	if hasCover && albumID.Valid {
		return albumCoverURL(albumID)
	}
	if url := coverArchiveURL(caa, release); url != nil {
		return url
	}
	if albumID.Valid {
		return albumCoverURL(albumID)
	}
	if fileID.Valid {
		return fileCoverURL(fileID)
	}
	return nil
}

// coverArchiveURL is the Cover Art Archive's front image for a release the
// library does not hold, sized for a list row. The archive redirects to the
// image; a release with no art answers 404 and the page shows nothing.
func coverArchiveURL(caa, release pgtype.Text) *string {
	id := caa
	if !id.Valid || id.String == "" {
		id = release
	}
	if !id.Valid || id.String == "" {
		return nil
	}
	s := "https://coverartarchive.org/release/" + id.String + "/front-250"
	return &s
}
