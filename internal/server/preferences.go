package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/sources"
)

// SourcePreferenceStore reads and writes what the user said to prefer among the
// copies peers are sharing, and what never to fetch.
type SourcePreferenceStore interface {
	SourcePreferences(context.Context) (db.SourcePreferencesRow, error)
	SaveSourcePreferences(
		ctx context.Context, saving db.SourcePreferencesRow,
	) (db.SourcePreferencesRow, error)
}

// knownFormats are the audio formats Schall can be told about. The list is what
// the screen offers and what a request is checked against: a preference naming
// a format no peer ever shares is a setting that silently does nothing, which
// is worse than being told the name was not recognised.
var knownFormats = []string{
	"flac", "wav", "aiff", "alac", "ape", "wv", "dsf", "dff",
	"mp3", "aac", "m4a", "ogg", "opus", "wma",
}

// maxBitRateFloor is the highest general floor that can be stored. It matches
// the constraint the table carries, so the refusal reads as a sentence rather
// than as a database error.
const maxBitRateFloor = 3000

// minBitRateFloor is the lowest floor worth writing. Below 32 kbps there is no
// music left to keep out, so a smaller number is a typing mistake rather than a
// preference.
const minBitRateFloor = 32

// formatBitRateCeiling is the highest floor each lossy format can reach: what
// the encoder itself tops out at. A floor above it refuses every copy that
// format can ever have, which is a way of saying "never fetch MP3" that does not
// look like one — the unacceptable list says it plainly.
var formatBitRateCeiling = map[string]int{
	"mp3":  320,
	"aac":  512,
	"m4a":  512,
	"ogg":  500,
	"opus": 510,
	"wma":  384,
}

type sourcePreferencesResponse struct {
	// Preferred are the formats to put first, best first.
	Preferred []string `json:"preferred"`
	// Unacceptable are the formats never to fetch.
	Unacceptable []string `json:"unacceptable"`
	// MinimumBitRate is the lowest bitrate a lossy copy may have, in kbps, where
	// its format has no floor of its own. Zero is no floor.
	MinimumBitRate int32 `json:"minimumBitRate"`
	// FormatMinimumBitRate is the floor for one named lossy format. A format
	// named here is held to its own number instead of MinimumBitRate.
	FormatMinimumBitRate map[string]int32 `json:"formatMinimumBitRate"`
	// Known is every format the screen may offer, so the list lives in one
	// place rather than in two that can disagree.
	Known []string `json:"known"`
	// Lossy is the subset of Known that carries a bitrate, with the highest
	// floor each of them can take. The screen draws a floor field for these and
	// for no others, reading the one list the ranking reads rather than keeping
	// a second one of its own.
	Lossy map[string]int32 `json:"lossy"`
}

// lossyCeilings is the response's Lossy field: every known format that has a
// bitrate, and the highest floor it can be given.
func lossyCeilings() map[string]int32 {
	ceilings := make(map[string]int32, len(knownFormats))
	for _, format := range knownFormats {
		if sources.Lossless(format) {
			continue
		}
		ceilings[format] = int32(ceilingFor(format))
	}
	return ceilings
}

// ceilingFor is the highest floor a format takes. A lossy format nobody has
// measured takes the general ceiling rather than being refused a floor: the list
// above is what encoders do, not what the setting is allowed to say.
func ceilingFor(format string) int {
	if ceiling, known := formatBitRateCeiling[format]; known {
		return ceiling
	}
	return maxBitRateFloor
}

func (api *API) getSourcePreferences(response http.ResponseWriter, request *http.Request) {
	if api.preferences == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"format preferences are unavailable", nil)
		return
	}
	row, err := api.preferences.SourcePreferences(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, preferencesResponse(row))
}

func preferencesResponse(row db.SourcePreferencesRow) sourcePreferencesResponse {
	floors := row.FormatMinimumBitRate
	if floors == nil {
		floors = map[string]int32{}
	}
	return sourcePreferencesResponse{
		Preferred: nonNil(row.Preferred), Unacceptable: nonNil(row.Unacceptable),
		MinimumBitRate: row.MinimumBitRate, FormatMinimumBitRate: floors,
		Known: knownFormats, Lossy: lossyCeilings(),
	}
}

type sourcePreferencesRequest struct {
	Preferred      []string `json:"preferred"`
	Unacceptable   []string `json:"unacceptable"`
	MinimumBitRate int32    `json:"minimumBitRate"`
	// FormatMinimumBitRate is a floor per lossy format. A format sent with zero
	// is a floor removed, which is how the screen clears a field.
	FormatMinimumBitRate map[string]int32 `json:"formatMinimumBitRate"`
}

func (api *API) saveSourcePreferences(response http.ResponseWriter, request *http.Request) {
	if api.preferences == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"format preferences are unavailable", nil)
		return
	}
	var input sourcePreferencesRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	preferred, problems := formatList(input.Preferred, "preferred")
	unacceptable, more := formatList(input.Unacceptable, "unacceptable")
	problems = append(problems, more...)
	// A format that is both preferred and refused is a setting that cannot do
	// what it says. Guessing which half was meant would be worse than saying so.
	for _, format := range preferred {
		if contains(unacceptable, format) {
			problems = append(problems,
				strings.ToUpper(format)+" cannot be both preferred and refused")
		}
	}
	if input.MinimumBitRate != 0 &&
		(input.MinimumBitRate < minBitRateFloor || input.MinimumBitRate > maxBitRateFloor) {
		problems = append(problems, fmt.Sprintf(
			"the bitrate floor must be 0 or between %d and %d kbps", minBitRateFloor, maxBitRateFloor))
	}
	floors, floorProblems := bitRateFloors(input.FormatMinimumBitRate)
	problems = append(problems, floorProblems...)
	if len(problems) > 0 {
		sort.Strings(problems)
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	row, err := api.preferences.SaveSourcePreferences(request.Context(), db.SourcePreferencesRow{
		Preferred: preferred, Unacceptable: unacceptable,
		MinimumBitRate: input.MinimumBitRate, FormatMinimumBitRate: floors,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, preferencesResponse(row))
}

// bitRateFloors tidies the per-format floors and names what it cannot use.
//
// A floor can be set only on a format that has a bitrate, and only inside the
// range that format's encoders reach. Everything outside that is a setting that
// cannot do what it says: a floor on FLAC would never apply, and a floor above
// what MP3 can encode would refuse every MP3 there is, which is a refusal the
// unacceptable list says plainly instead.
//
// Zero removes a floor. It is how the screen clears the field, and it is stored
// as nothing rather than as a floor of zero.
func bitRateFloors(sent map[string]int32) (map[string]int32, []string) {
	floors := make(map[string]int32, len(sent))
	var problems []string
	for name, floor := range sent {
		format := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), ".")
		switch {
		case format == "":
			continue
		case !contains(knownFormats, format):
			problems = append(problems,
				"minimum bit rate: "+format+" is not an audio format Schall knows")
		case sources.Lossless(format):
			problems = append(problems, strings.ToUpper(format)+
				" keeps every bit of the recording, so it has no bit rate to set a floor on")
		case floor == 0:
			continue
		case floor < minBitRateFloor || int(floor) > ceilingFor(format):
			problems = append(problems, fmt.Sprintf(
				"the %s floor must be between %d and %d kbps",
				strings.ToUpper(format), minBitRateFloor, ceilingFor(format)))
		default:
			floors[format] = floor
		}
	}
	return floors, problems
}

// formatList tidies what was sent and names what it cannot use. Case and a
// leading dot are the two ways the same format arrives written differently, and
// a name repeated is kept once in the order it was first given, because the
// order is the whole of what "preferred" means.
func formatList(values []string, field string) ([]string, []string) {
	cleaned := make([]string, 0, len(values))
	var problems []string
	for _, value := range values {
		format := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
		if format == "" {
			continue
		}
		if !contains(knownFormats, format) {
			problems = append(problems, field+": "+format+" is not an audio format Schall knows")
			continue
		}
		if !contains(cleaned, format) {
			cleaned = append(cleaned, format)
		}
	}
	return cleaned, problems
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// nonNil answers with an empty list rather than a null, so the interface has a
// list to iterate whether or not anybody has said anything.
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
