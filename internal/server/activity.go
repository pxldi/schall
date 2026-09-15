package server

import (
	"context"
	"net/http"
	"time"

	"github.com/pxldi/schall/internal/activity"
)

// ActivityReader answers what Schall has been doing lately. Optional: without
// it the route reports itself unavailable and every other screen is unchanged.
type ActivityReader interface {
	Since(ctx context.Context, since time.Time) ([]activity.Event, error)
}

// WithActivity names the reader behind the Overview feed.
func WithActivity(reader ActivityReader) Option {
	return func(api *API) { api.activity = reader }
}

// listActivity answers the last day of arrivals, refusals, questions and
// removals, newest first.
//
// The window is fixed rather than asked for. The screen this feeds says what
// happened while somebody was away, and a day is what that means; a parameter
// would invite a caller to ask for a year of it and read the answer as a page
// of history the interface has no room for.
func (api *API) listActivity(response http.ResponseWriter, request *http.Request) {
	if api.activity == nil {
		api.problem(response, http.StatusServiceUnavailable, "the activity feed is unavailable",
			[]string{"This installation cannot read what has been happening."})
		return
	}
	events, err := api.activity.Since(request.Context(), time.Now().Add(-activity.Window))
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"items": events,
		// Said rather than assumed, so the screen can name the window it is
		// showing instead of the reader having to know it.
		"windowHours": int(activity.Window / time.Hour),
	})
}
