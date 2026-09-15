package slskd

import (
	"net/http"

	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// Builder constructs slskd providers from stored settings.
//
// Gate is shared by every client the builder makes, and is what keeps searching
// inside what slskd tolerates at once and what the Soulseek server tolerates in
// a row. A builder without one gives each client its own, which bounds each
// search against nothing but itself.
//
// Logger is passed to every client, which reports what each search's window
// spent its time on. A builder without one measures nothing.
type Builder struct {
	HTTPClient *http.Client
	Gate       *Gate
	Logger     zerolog.Logger
}

func (builder Builder) Build(settings sources.Settings) (sources.Provider, error) {
	return NewClient(Options{
		BaseURL:       settings.BaseURL,
		APIKey:        settings.APIKey,
		HTTPClient:    builder.HTTPClient,
		SearchTimeout: settings.SearchTimeout,
		Gate:          builder.Gate,
		Logger:        builder.Logger,
	})
}
