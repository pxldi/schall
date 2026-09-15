package server

import (
	"fmt"
	"net/http"
	"time"
)

// heartbeatInterval keeps the connection warm. A stream that says nothing for
// long enough is closed by proxies and by browsers, and a download that takes
// twenty minutes says nothing for most of them. A variable so a test does not
// have to wait it out.
var heartbeatInterval = 25 * time.Second

// streamEvents holds a connection open and writes a line whenever something the
// interface displays has changed.
//
// It is registered outside the request timeout that everything else has, because
// staying open is the entire point of it. That is also why it writes its own
// heartbeat rather than relying on the connection being left alone.
//
// The stream carries invalidations, never state: it says "downloads changed",
// and the client refetches through the same endpoints it already uses. A second
// path carrying state would be a second thing that can disagree with the
// database, and the interface would have no way to tell which was right.
func (api *API) streamEvents(response http.ResponseWriter, request *http.Request) {
	if api.events == nil {
		api.problem(response, http.StatusServiceUnavailable, "event streaming is unavailable", nil)
		return
	}

	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	// Tell nginx and friends not to buffer this, which would hold every notice
	// until the response ended — that is, until it no longer mattered.
	response.Header().Set("X-Accel-Buffering", "no")

	controller := http.NewResponseController(response)
	// The write deadline has to be lifted: a stream that lives for an hour would
	// otherwise be cut off by the server's own default.
	_ = controller.SetWriteDeadline(time.Time{})

	notices, stop := api.events.Subscribe()
	defer stop()

	// An opening comment flushes the headers, so the browser reports the stream
	// as open immediately rather than when the first real notice arrives.
	if _, err := fmt.Fprint(response, ": connected\n\n"); err != nil {
		return
	}
	if err := controller.Flush(); err != nil {
		// Without flushing, every notice would arrive at once at the end, which
		// is worse than not offering the stream at all.
		api.logger.Warn().Err(err).Msg("event stream cannot flush; falling back to polling")
		return
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-request.Context().Done():
			return
		case topic, open := <-notices:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(response, "event: %s\ndata: {}\n\n", topic); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(response, ": keep-alive\n\n"); err != nil {
				return
			}
		}
		if err := controller.Flush(); err != nil {
			return
		}
	}
}
