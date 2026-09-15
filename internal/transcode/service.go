package transcode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pxldi/schall/internal/tagging"
	"github.com/rs/zerolog"
)

// Request is one proven file about to be filed, as the import knows it: where
// the bytes are now, the name they will have in the library, and how many of
// them there are.
type Request struct {
	Source    string
	Name      string
	SizeBytes int64
}

// Placement is what to write for one Request.
//
// It is the answer whether or not anything was re-encoded. A file nothing was
// done to comes back naming its own source, its own name and its own size, so
// an import that never asked for smaller copies and one that asked and was
// refused walk exactly the same code afterwards.
type Placement struct {
	// Source is the bytes to copy: the file that arrived, or the smaller copy
	// made from it. It is never the provider's file once a re-encode succeeded,
	// and the provider's file is never deleted by anything here.
	Source string
	// Name is the name in the library. It carries the target's extension when
	// the copy was re-encoded, so a FLAC that became an MP3 is filed as one.
	Name      string
	SizeBytes int64
	// FromFormat and OriginalSizeBytes are set only on a copy that was
	// re-encoded, and are what the file row reads back as "From FLAC" and the
	// room that bought.
	FromFormat        string
	OriginalSizeBytes int64
	// Note says why a re-encode the policy asked for did not happen. It is
	// empty on every other placement, including every placement of an import
	// that never asked for one.
	Note string
}

// Transcoded reports whether this placement is a smaller copy rather than the
// file that arrived.
func (placement Placement) Transcoded() bool { return placement.FromFormat != "" }

// Service plans the placements of one import.
//
// The policy is read per import rather than held, for the same reason the
// folder layout is: changing the setting takes effect on the next import
// instead of on the next restart.
type Service struct {
	policy  func(context.Context) (Policy, error)
	encoder *Encoder
	logger  zerolog.Logger
}

func NewService(
	policy func(context.Context) (Policy, error), binary string, logger zerolog.Logger,
) *Service {
	return &Service{policy: policy, encoder: NewEncoder(binary), logger: logger}
}

// Policy reads the operator's current answer. It is the same read Plan makes
// internally; a caller that wants to decide something before spending a file
// open on it — whether to run a batch at all — reads it here instead.
func (service *Service) Policy(ctx context.Context) (Policy, error) {
	if service == nil {
		return Policy{}, nil
	}
	return service.policy(ctx)
}

// EncoderAvailable reports whether ffmpeg can be found, without running it or
// reading the policy. A nil Service — a build with no encoder configured —
// reports false, which is what every caller should do about it.
func (service *Service) EncoderAvailable() bool {
	if service == nil {
		return false
	}
	return service.encoder.Available()
}

// Plan says what to write for every file of one import, and returns the
// cleanup that removes whatever it encoded.
//
// Nothing here can fail an import. A policy that cannot be read, an encoder
// that is not installed, an encode that fails, a copy that came out the wrong
// length: every one of them places the file that arrived, unchanged, and says
// so in the placement's Note. Losing the music to save room on it would be the
// worst possible trade.
//
// A nil Service plans an import that re-encodes nothing, which is what a build
// without an encoder configured does.
func (service *Service) Plan(ctx context.Context, requests []Request) ([]Placement, func()) {
	placements := make([]Placement, len(requests))
	for index, request := range requests {
		placements[index] = Placement{
			Source: request.Source, Name: request.Name, SizeBytes: request.SizeBytes,
		}
	}
	nothingToClean := func() {}
	if service == nil || len(requests) == 0 {
		return placements, nothingToClean
	}

	policy, err := service.policy(ctx)
	if err != nil {
		service.logger.Error().Err(err).
			Msg("the transcoding policy could not be read; the files were imported as they arrived")
		return placements, nothingToClean
	}
	if !policy.Enabled {
		return placements, nothingToClean
	}

	chosen, taken := service.choose(policy, requests, placements)
	if len(chosen) == 0 {
		return placements, nothingToClean
	}

	// One scratch directory for the whole import, outside the library so no scan
	// ever walks into a copy that is still being made.
	work, err := os.MkdirTemp("", "schall-transcode-")
	if err != nil {
		service.logger.Error().Err(err).
			Msg("no scratch space for re-encoding; the files were imported as they arrived")
		return placements, nothingToClean
	}
	cleanup := func() { _ = os.RemoveAll(work) }

	for _, candidate := range chosen {
		request := requests[candidate.index]
		name := replaceExtension(request.Name, policy.Target)
		if _, clash := taken[strings.ToLower(name)]; clash {
			placements[candidate.index].Note = fmt.Sprintf(
				"Re-encoding %q would have taken the name of another file in this import, so it was imported as it arrived.",
				request.Name)
			continue
		}
		encoded := filepath.Join(work, fmt.Sprintf("%d.%s", candidate.index, policy.Target))
		if note := service.encode(ctx, policy, request, candidate.properties, encoded); note != "" {
			placements[candidate.index].Note = note
			continue
		}
		info, err := os.Stat(encoded)
		if err != nil {
			placements[candidate.index].Note =
				"The smaller copy could not be read back, so the file was imported as it arrived."
			continue
		}
		// A copy that is not smaller is the whole reason for the setting
		// undone: it would cost a generation of quality and buy no room. It
		// happens to quiet or sparse music, where the lossless file is already
		// tiny and a fixed rate is not.
		if info.Size() >= request.SizeBytes {
			placements[candidate.index].Note = fmt.Sprintf(
				"%q would be no smaller in %s, so it was imported as it arrived.",
				request.Name, strings.ToUpper(policy.Target))
			continue
		}
		taken[strings.ToLower(name)] = struct{}{}
		placements[candidate.index] = Placement{
			Source:            encoded,
			Name:              name,
			SizeBytes:         info.Size(),
			FromFormat:        Format(request.Name),
			OriginalSizeBytes: request.SizeBytes,
		}
	}
	return placements, cleanup
}

// candidate is one file the policy asked for, together with what its stream
// turned out to be. The properties travel with it because the length of the
// original is what the smaller copy is checked against, and reading it twice
// would open every file in the release a second time.
type candidate struct {
	index      int
	properties tagging.AudioProperties
}

// choose picks the files the policy asks for and reserves the names of the
// files it does not. A release holding both `Track.flac` and `Track.mp3` would
// otherwise re-encode the first onto the second, and one of two different
// recordings would quietly not be imported.
func (service *Service) choose(
	policy Policy, requests []Request, placements []Placement,
) ([]candidate, map[string]struct{}) {
	taken := make(map[string]struct{}, len(requests))
	var chosen []candidate
	for index, request := range requests {
		properties, err := tagging.ReadProperties(request.Source)
		if err != nil {
			service.logger.Debug().Err(err).Str("file", request.Name).
				Msg("the audio could not be read, so it was imported as it arrived")
		}
		if policy.Applies(request.Name, properties) {
			chosen = append(chosen, candidate{index: index, properties: properties})
			continue
		}
		taken[strings.ToLower(placements[index].Name)] = struct{}{}
	}
	return chosen, taken
}

// encode makes one smaller copy and checks it, returning the sentence to record
// when it could not be made or could not be trusted.
//
// The check is the length. An encoder that ran out of disc, was killed, or was
// handed a file it only partly understood produces a file that plays and stops
// early, and a short copy is the one failure that would otherwise be imported
// as though it were the music.
func (service *Service) encode(
	ctx context.Context, policy Policy, request Request,
	source tagging.AudioProperties, destination string,
) string {
	if err := service.encoder.Encode(ctx, policy, request.Source, destination); err != nil {
		service.logger.Warn().Err(err).Str("file", request.Name).
			Msg("a smaller copy could not be made; the file was imported as it arrived")
		return "A smaller copy could not be made, so the file was imported as it arrived."
	}
	written, err := tagging.ReadProperties(destination)
	if err != nil || !written.Known() {
		return "The smaller copy is not audio anything can read, so the file was imported as it arrived."
	}
	if difference(written.DurationMS, source.DurationMS) > ToleranceMS {
		service.logger.Warn().Str("file", request.Name).
			Int("source_ms", source.DurationMS).Int("encoded_ms", written.DurationMS).
			Msg("a smaller copy came out the wrong length; the file was imported as it arrived")
		return "The smaller copy is not the same length as the file, so the file was imported as it arrived."
	}
	return ""
}

func replaceExtension(name, format string) string {
	return strings.TrimSuffix(name, filepath.Ext(name)) + "." + format
}

func difference(left, right int) int {
	if left > right {
		return left - right
	}
	return right - left
}
