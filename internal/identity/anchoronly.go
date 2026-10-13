package identity

import (
	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/tagmatch"
)

// MethodPublishedSample is the method a copy admitted on its want's anchor
// carries. It is the grade the anchor has on the recording path, so a copy
// proven either way reads the same (bySample).
var MethodPublishedSample = bySample.method()

// SourceVerification is what one copy came to against a want MusicBrainz has
// no recording for: the want's anchor, and the entry it was asked for in
// (ADR 0037 §3).
type SourceVerification struct {
	// Admitted says the copy reproduces an anchor that can admit it and
	// nothing holds it back.
	Admitted bool
	// Refused says a Deezer preview or a keyed address's excerpt was measured
	// and the copy is other audio.
	Refused bool
	// Contradicted says the copy's own tags or measured length disagree with
	// the entry.
	Contradicted bool
	// NamesARecording says the copy carries a MusicBrainz recording ID. The want
	// has no recording for it to agree with, and MusicBrainz knowing the file
	// means resolution has not caught up yet.
	NamesARecording bool
	// AnchorAgrees and AnchorSaysOtherwise are the anchor's own reading, kept for
	// the copy's evidence whatever the verdict.
	AnchorAgrees        bool
	AnchorSaysOtherwise bool
	Agrees              []string
	Differs             []string
	Summary             string
}

// VerifyAgainstAnchor asks whether one copy is the track a want with no
// recording names, by its anchor alone.
//
// The anchor is the only thing here that can admit. It admits when it is a
// Deezer preview, the artist's Topic upload or the excerpt from the address a
// person keyed the want to (ADR 0038), and the copy reproduces it under the
// chromaprint thresholds. The rest can only hold the copy back: a title,
// artist, ISRC or length that contradicts the entry holds it whatever the
// audio says, and a MusicBrainz recording ID on the copy holds it too. Tags
// that agree admit nothing. A Deezer preview or a keyed excerpt the copy fails
// to reproduce refuses it, before anything else is read, as it does on the
// recording path.
//
// file.Anchor carries no recording: it was fetched for the entry.
func VerifyAgainstAnchor(file, entry Evidence) SourceVerification {
	anchor, source := tagmatch.Unknown, ""
	if file.Anchor != nil {
		anchor, source = readAnchorComparison(file.Anchor), file.Anchor.Source
	}
	admitting := source == AnchorSourceDeezer || source == AnchorByNameTopic ||
		source == AnchorSourceKeyed

	pair := comparison{
		isrc:   tagmatch.Identifier(file.ISRC, entry.ISRC),
		artist: tagmatch.Credit(file.Artist, entry.Artist),
		title:  tagmatch.Title(file.Title, entry.Title),
	}
	if file.DurationMS > 0 && entry.DurationMS > 0 {
		pair.durationDeltaMS = abs(file.DurationMS - entry.DurationMS)
		pair.duration = tagmatch.Agrees
		if pair.durationDeltaMS > DurationToleranceMS {
			pair.duration = tagmatch.Differs
		}
	}

	verification := SourceVerification{
		AnchorAgrees:        admitting && anchor == tagmatch.Agrees,
		AnchorSaysOtherwise: anchor == tagmatch.Differs,
		NamesARecording:     file.RecordingID != uuid.Nil,
		Contradicted: pair.isrc == tagmatch.Differs || pair.artist == tagmatch.Differs ||
			pair.title == tagmatch.Differs || pair.duration == tagmatch.Differs,
	}
	verification.Agrees, verification.Differs = describeAgainstEntry(pair, anchor, source)

	switch {
	case verification.AnchorSaysOtherwise:
		verification.Refused = true
		verification.Summary = "Audio doesn't match the Deezer preview."
		if source == AnchorSourceKeyed {
			verification.Summary = "Audio doesn't match the linked track."
		}
	case verification.NamesARecording:
		verification.Summary = "File carries a MusicBrainz recording ID."
	case verification.Contradicted:
		verification.Summary = candidateSentence(
			SubjectEntry, pair, verification.Agrees, verification.Differs, "")
	case verification.AnchorAgrees:
		verification.Admitted = true
		verification.Summary = "Audio matches the Deezer preview. Not on MusicBrainz."
		switch source {
		case AnchorByNameTopic:
			verification.Summary = "Audio matches the artist's YouTube Topic upload. Not on MusicBrainz."
		case AnchorSourceKeyed:
			verification.Summary = "Audio matches the linked track."
		}
	default:
		verification.Summary = "No sample proves it."
	}
	return verification
}

// describeAgainstEntry lists what agreed and what disagreed between a copy and
// the entry, in the words describeVerdicts uses for a recording.
func describeAgainstEntry(
	pair comparison, anchor tagmatch.Verdict, source string,
) (agreements, disagreements []string) {
	audio := "audio (matches the published sample of this track)"
	switch {
	case source == AnchorByNameTopic:
		audio = "audio (matches the artist's Topic upload)"
	case source == AnchorSourceKeyed && anchor == tagmatch.Differs:
		audio = "audio (not the track at the keyed address)"
	case source == AnchorSourceKeyed:
		audio = "audio (matches the track at the keyed address)"
	case anchor == tagmatch.Differs:
		audio = "audio (not the published sample of this track)"
	}
	labelled := []struct {
		result tagmatch.Verdict
		label  string
	}{
		{anchor, audio},
		{pair.isrc, "ISRC"}, {pair.title, "title"}, {pair.artist, "artist"},
		{pair.duration, durationLabel(pair)},
	}
	agreements, disagreements = []string{}, []string{}
	for _, entry := range labelled {
		switch entry.result {
		case tagmatch.Agrees:
			agreements = append(agreements, entry.label)
		case tagmatch.Differs:
			disagreements = append(disagreements, entry.label)
		}
	}
	return agreements, disagreements
}
