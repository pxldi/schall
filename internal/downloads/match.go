package downloads

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/tagmatch"
)

// Matching a downloaded folder against a catalogue edition is the one moment
// where Schall decides that audio it did not produce is audio the user asked
// for. A wrong answer imports the wrong recording and then remembers it as
// provenance, so the rule here is deliberately one-sided: the evidence has to
// identify exactly one track and nothing may contradict it. Everything short of
// that becomes a question for review rather than a guess.
//
// Matching runs in three stages:
//
//  1. Compare every downloaded file with every catalogue track, one kind of
//     evidence at a time. A tag neither side carries is silence, never
//     agreement.
//  2. Grade each pair. A grade is conclusive only when an identifying
//     combination agrees and no other evidence contradicts it.
//  3. Assign the strongest grades first, and only where the choice is mutual:
//     one track for that file and one file for that track. A contested pair
//     stays unassigned and is reported with the candidates that contested it.
//
// Position is treated as ordinary evidence rather than as the question. A file
// ripped from the album a single was later collected on carries that album's
// track number, and refusing it for that reason alone was the most common way
// a correct download stopped for review.

// durationToleranceMS is how far two durations may sit apart and still be read
// as the same recording. It absorbs encoder and tagger rounding, not the
// difference between an album version and a live one.
const durationToleranceMS = 5000

// maxCandidates caps how many near misses are reported for one file. Review
// needs the plausible answers, not the whole edition.
const maxCandidates = 3

// verdict is what one kind of evidence says about a file and a track. The
// vocabulary and the rules behind it are shared with library resolution, so
// that both places Schall decides identity mean the same thing by agreement.
type verdict = tagmatch.Verdict

const (
	unknown = tagmatch.Unknown
	agrees  = tagmatch.Agrees
	differs = tagmatch.Differs
)

// comparison is everything one downloaded file and one catalogue track have to
// say to each other.
type comparison struct {
	recording verdict
	isrc      verdict
	artist    verdict
	album     verdict
	title     verdict
	position  verdict
	duration  verdict
	// durationDeltaMS is how far the two durations sit apart, when both sides
	// carry one.
	durationDeltaMS int
}

// contradicted reports evidence that argues these are different recordings.
// Identifiers and artist disagree only when something is genuinely wrong, and a
// duration outside the tolerance is a different performance. A different album,
// position, or title is ordinary between editions and never a contradiction on
// its own.
func (pair comparison) contradicted() bool {
	return pair.recording == differs || pair.isrc == differs ||
		pair.artist == differs || pair.duration == differs
}

// linked reports whether anything ties this file to this track at all.
// Duration and album are corroboration; alone they are a coincidence between
// two three-minute songs.
func (pair comparison) linked() bool {
	return pair.recording == agrees || pair.isrc == agrees ||
		pair.title == agrees || pair.position == agrees
}

// grade ranks how strongly one comparison identifies a track. The order is the
// order in which matching trusts evidence, and every grade above candidateOnly
// is strong enough to assign a file on its own.
type grade int

const (
	noMatch grade = iota
	candidateOnly
	byTitleAndDuration
	byPositionAndTitle
	byISRC
	byRecordingID
)

const weakestConclusive = byTitleAndDuration

func (level grade) conclusive() bool { return level >= weakestConclusive }

func (level grade) method() string {
	switch level {
	case byRecordingID:
		return "recording-id"
	case byISRC:
		return "isrc"
	case byPositionAndTitle:
		return "position-and-title"
	case byTitleAndDuration:
		return "title-and-duration"
	}
	return ""
}

func (level grade) summary() string {
	switch level {
	case byRecordingID:
		return "matched by MusicBrainz recording ID"
	case byISRC:
		return "matched by ISRC"
	case byPositionAndTitle:
		return "matched by disc and track position and title"
	case byTitleAndDuration:
		return "matched by title and duration"
	}
	return ""
}

// gradeOf decides how much one comparison is worth.
//
// Nothing is conclusive while any evidence contradicts it, including a
// contradiction of a proven identifier: a file whose recording ID agrees but
// whose duration is a minute out is mistagged, not identified, and that is a
// question for a person.
func gradeOf(pair comparison) grade {
	if pair.contradicted() {
		return candidateGrade(pair)
	}
	switch {
	case pair.recording == agrees:
		return byRecordingID
	case pair.isrc == agrees:
		return byISRC
	// An absent album is silence, so position needs the album or duration to
	// corroborate it. Track four of one release and track four of another are
	// unrelated numbers that happen to be equal.
	case pair.position == agrees && pair.title == agrees &&
		(pair.album == agrees || pair.duration == agrees):
		return byPositionAndTitle
	// The case position alone cannot answer: the same recording collected on a
	// different release, carrying that release's track number.
	case pair.title == agrees && pair.duration == agrees:
		return byTitleAndDuration
	}
	return candidateGrade(pair)
}

func candidateGrade(pair comparison) grade {
	if pair.linked() {
		return candidateOnly
	}
	return noMatch
}

// compare reads one downloaded file against one catalogue track. The catalogue
// side carries the release artist and title, so a file that names a different
// release is visible as such rather than as a missing comparison.
//
// The file's duration is whatever reading it produced: a FLAC's own STREAMINFO,
// and for everything else the `TLEN` the file claims. It is deliberately not
// fpcalc's measurement, which on this path does not exist yet — a folder is
// matched first and only its matched files are then fingerprinted, so the
// measurement arrives after the comparison that would have used it. Decoding
// every file of every folder before matching in order to have it earlier is a
// different change with a different cost, and reordering the two would change
// which files are admitted.
func compare(file, track db.ImportTags) comparison {
	pair := comparison{
		recording: sameIdentifier(file.MusicBrainzRecordingID, track.MusicBrainzRecordingID),
		isrc:      sameIdentifier(file.ISRC, track.ISRC),
		artist:    sameArtist(file.Artist, track.Artist),
		album:     sameTitleTag(file.Album, track.Album),
		title:     sameTitleTag(file.Title, track.Title),
		position:  samePosition(file, track),
	}
	if file.DurationMS > 0 && track.DurationMS > 0 {
		pair.durationDeltaMS = abs(file.DurationMS - track.DurationMS)
		pair.duration = agrees
		if pair.durationDeltaMS > durationToleranceMS {
			pair.duration = differs
		}
	}
	return pair
}

func sameIdentifier(left, right string) verdict { return tagmatch.Identifier(left, right) }

func sameTitleTag(left, right string) verdict { return tagmatch.Title(left, right) }

// samePosition compares disc and track number. A file with no track number is
// silent about position rather than at position zero.
func samePosition(file, track db.ImportTags) verdict {
	if file.TrackNumber == 0 || track.TrackNumber == 0 {
		return unknown
	}
	if file.DiscNumber == track.DiscNumber && file.TrackNumber == track.TrackNumber {
		return agrees
	}
	return differs
}

// sameArtist compares a file's artist tag with the release artist.
func sameArtist(observed, expected string) verdict { return tagmatch.Credit(observed, expected) }

// fileTags is one downloaded file as matching sees it: a name to report it by
// and the tags read from it.
type fileTags struct {
	name string
	tags db.ImportTags
}

// fileMatch is what matching concluded about one downloaded file.
type fileMatch struct {
	// track indexes the catalogue track this file was matched to, or -1 when
	// the evidence did not identify one.
	track int
	// match describes how the file was matched, when it was.
	match *db.ImportMatch
	// candidates are the tracks the file might be, best first, when it was not
	// matched. They are what review needs in order to answer.
	candidates []db.ImportCandidate
	// problem is why the file was not matched, phrased for review. It is empty
	// exactly when the file was matched.
	problem string
}

type matcher struct {
	files  []fileTags
	tracks []db.ImportTags
	pairs  [][]comparison
	grades [][]grade
	// fileOwner holds the track each file was assigned, or -1.
	fileOwner []int
	// trackHolder holds the name of the file that owns each track. A track
	// reserved by a recorded decision starts out held.
	trackHolder []string
	// reserved is the subset of those tracks a person decided, kept apart so
	// that a file losing to a decision is told so.
	reserved map[int]string
}

// matchRelease ties downloaded files to the tracks of one catalogue edition.
// Tracks named in reserved were already settled by a reviewer's decision and
// are not available to matching, which may only ever add agreement, never
// overrule a person.
func matchRelease(files []fileTags, tracks []db.ImportTags, reserved map[int]string) []fileMatch {
	work := &matcher{
		files: files, tracks: tracks,
		pairs:       make([][]comparison, len(files)),
		grades:      make([][]grade, len(files)),
		fileOwner:   make([]int, len(files)),
		trackHolder: make([]string, len(tracks)),
		reserved:    reserved,
	}
	for index, name := range reserved {
		work.trackHolder[index] = name
	}
	for file := range files {
		work.fileOwner[file] = -1
		work.pairs[file] = make([]comparison, len(tracks))
		work.grades[file] = make([]grade, len(tracks))
		for track := range tracks {
			work.pairs[file][track] = compare(files[file].tags, tracks[track])
			work.grades[file][track] = gradeOf(work.pairs[file][track])
		}
	}
	work.assign()

	result := make([]fileMatch, len(files))
	for file := range files {
		result[file] = work.conclude(file)
	}
	return result
}

// assign settles the pairs the evidence chooses on its own. It works from the
// strongest grade down, so a file proven by an identifier is out of the pool
// before weaker evidence is considered at all, and within one grade it accepts
// a pair only when file and track are each other's only option. Removing a
// settled pair can leave a neighbour uncontested, so each grade is worked until
// it stops producing agreement.
//
// The result does not depend on the order the files arrive in. Settling a pair
// can only ever free a file or a track that was waiting on it: a track two
// files reach with equal strength is refused to both of them, so no assignment
// can take a track another file would otherwise have been given.
//
// A pass that settles nothing ends the grade, so the passes across all grades
// are bounded by the number of files, and the folder itself is bounded when the
// request is recorded.
func (work *matcher) assign() {
	for level := byRecordingID; level >= weakestConclusive; level-- {
		for settled := true; settled; {
			settled = false
			for file := range work.files {
				track, ok := work.onlyChoice(file, level)
				if !ok || !work.onlyClaimant(file, track, level) {
					continue
				}
				work.fileOwner[file] = track
				work.trackHolder[track] = work.files[file].name
				settled = true
			}
		}
	}
}

// onlyChoice reports the one free track this file reaches at this grade.
func (work *matcher) onlyChoice(file int, level grade) (int, bool) {
	if work.fileOwner[file] >= 0 {
		return 0, false
	}
	choice, found := -1, 0
	for track := range work.tracks {
		if work.trackHolder[track] != "" || work.grades[file][track] != level {
			continue
		}
		choice, found = track, found+1
	}
	return choice, found == 1
}

// onlyClaimant reports whether this file is the only unsettled file reaching
// that track at this grade. Agreement has to run both ways: two files with
// equal evidence for one track identify nothing.
func (work *matcher) onlyClaimant(file, track int, level grade) bool {
	for other := range work.files {
		if other == file || work.fileOwner[other] >= 0 {
			continue
		}
		if work.grades[other][track] == level {
			return false
		}
	}
	return true
}

// conclude describes the outcome for one file, including why an unmatched file
// was not matched and which tracks it could still be.
func (work *matcher) conclude(file int) fileMatch {
	if track := work.fileOwner[file]; track >= 0 {
		level := work.grades[file][track]
		return fileMatch{track: track, match: &db.ImportMatch{
			Method:  level.method(),
			Summary: level.summary(),
			Notes:   matchNotes(work.pairs[file][track], work.files[file].tags, work.tracks[track]),
		}}
	}
	result := fileMatch{track: -1, candidates: work.candidatesFor(file)}
	result.problem = work.explain(file, result.candidates)
	return result
}

// explain says why a file was not matched, in the terms a reviewer needs in
// order to answer. The three cases are genuinely different questions: too many
// answers, an answer another file also has, and no answer at all.
func (work *matcher) explain(file int, candidates []db.ImportCandidate) string {
	name := work.files[file].name
	var conclusive []int
	for track := range work.tracks {
		if work.grades[file][track].conclusive() {
			conclusive = append(conclusive, track)
		}
	}
	switch {
	case len(conclusive) > 1:
		return fmt.Sprintf("%q matches %s equally well; the evidence does not choose between them",
			name, joinLabels(work.tracks, conclusive))
	case len(conclusive) == 1:
		track := conclusive[0]
		label := trackLabel(work.tracks[track])
		rivals := work.rivalsFor(file, track)
		switch {
		case work.reserved[track] != "":
			return fmt.Sprintf("%q matches catalogue track %s, which was already resolved to %q",
				name, label, work.reserved[track])
		case work.trackHolder[track] != "":
			return fmt.Sprintf("%q matches catalogue track %s, which %q matched more conclusively",
				name, label, work.trackHolder[track])
		case len(rivals) > 0:
			return fmt.Sprintf("%q and %s both match catalogue track %s; the evidence does not choose between them",
				name, joinWords(rivals), label)
		}
		return fmt.Sprintf("%q matches catalogue track %s, but the evidence did not settle it", name, label)
	case len(candidates) > 0:
		closest := candidates[0]
		reason := "the evidence does not identify it conclusively"
		if len(closest.Differs) > 0 {
			verb := " disagrees"
			if len(closest.Differs) > 1 {
				verb = " disagree"
			}
			reason = "its " + joinWords(closest.Differs) + verb
		}
		return fmt.Sprintf("%q is not conclusively any track of this edition; the closest is %s, but %s",
			name, trackLabel(closest.Track), reason)
	}
	return fmt.Sprintf("%q matches no track of this edition", name)
}

// rivalsFor names the other unmatched files that also reach this track
// conclusively. They are the reason it stayed unmatched.
func (work *matcher) rivalsFor(file, track int) []string {
	rivals := []string{}
	for other := range work.files {
		if other == file || work.fileOwner[other] >= 0 {
			continue
		}
		if work.grades[other][track].conclusive() {
			rivals = append(rivals, fmt.Sprintf("%q", work.files[other].name))
		}
	}
	return rivals
}

// candidatesFor ranks the tracks an unmatched file might be, so review is given
// the plausible answers together with what stands against each of them.
func (work *matcher) candidatesFor(file int) []db.ImportCandidate {
	type ranked struct {
		candidate db.ImportCandidate
		score     int
		delta     int
		track     int
	}
	var found []ranked
	for track := range work.tracks {
		if work.grades[file][track] == noMatch {
			continue
		}
		pair := work.pairs[file][track]
		agreements, disagreements := describeVerdicts(pair)
		candidate := db.ImportCandidate{
			Track: work.tracks[track], Agrees: agreements, Differs: disagreements,
		}
		if holder := work.trackHolder[track]; holder != "" && holder != work.files[file].name {
			candidate.TakenBy = holder
		}
		found = append(found, ranked{
			candidate: candidate, score: strengthOf(pair),
			delta: pair.durationDeltaMS, track: track,
		})
	}
	sort.SliceStable(found, func(left, right int) bool {
		if found[left].score != found[right].score {
			return found[left].score > found[right].score
		}
		if found[left].delta != found[right].delta {
			return found[left].delta < found[right].delta
		}
		return found[left].track < found[right].track
	})
	if len(found) > maxCandidates {
		found = found[:maxCandidates]
	}
	candidates := make([]db.ImportCandidate, 0, len(found))
	for _, entry := range found {
		candidates = append(candidates, entry.candidate)
	}
	return candidates
}

// strengthOf ranks near misses for display only. It never decides whether a
// pair may be imported; gradeOf does that, and it does not add evidence up.
func strengthOf(pair comparison) int {
	weights := []struct {
		result verdict
		weight int
	}{
		{pair.recording, 100}, {pair.isrc, 80}, {pair.title, 40},
		{pair.position, 20}, {pair.duration, 10}, {pair.artist, 5}, {pair.album, 5},
	}
	score := 0
	for _, entry := range weights {
		switch entry.result {
		case agrees:
			score += entry.weight
		case differs:
			score -= entry.weight
		}
	}
	return score
}

// describeVerdicts names what agreed and what did not, in the order a person
// would weigh it.
func describeVerdicts(pair comparison) (agreements, disagreements []string) {
	labelled := []struct {
		result verdict
		label  string
	}{
		{pair.recording, "MusicBrainz recording ID"}, {pair.isrc, "ISRC"},
		{pair.title, "title"}, {pair.artist, "artist"}, {pair.album, "album"},
		{pair.position, "disc and track position"}, {pair.duration, durationLabel(pair)},
	}
	agreements, disagreements = []string{}, []string{}
	for _, entry := range labelled {
		switch entry.result {
		case agrees:
			agreements = append(agreements, entry.label)
		case differs:
			disagreements = append(disagreements, entry.label)
		}
	}
	return agreements, disagreements
}

func durationLabel(pair comparison) string {
	if pair.duration == differs {
		return fmt.Sprintf("duration (%s out)", roughSeconds(pair.durationDeltaMS))
	}
	return "duration"
}

// matchNotes records what a matched file says that the catalogue does not,
// without calling it a problem. A file collected from another release is still
// the right recording, and the import history should say where it came from.
func matchNotes(pair comparison, file, track db.ImportTags) []string {
	notes := []string{}
	if pair.position == differs {
		notes = append(notes, fmt.Sprintf("the file is tagged as position %s, the catalogue as %s",
			position(file.DiscNumber, file.TrackNumber), position(track.DiscNumber, track.TrackNumber)))
	}
	if pair.album == differs {
		notes = append(notes, fmt.Sprintf("the file is tagged as part of %q", file.Album))
	}
	if pair.title == differs {
		notes = append(notes, fmt.Sprintf("the file is titled %q", file.Title))
	}
	if pair.duration == agrees && pair.durationDeltaMS > 0 {
		notes = append(notes, fmt.Sprintf("duration is %s from the catalogue",
			roughSeconds(pair.durationDeltaMS)))
	}
	return notes
}

func roughSeconds(milliseconds int) string {
	seconds := (milliseconds + 500) / 1000
	if seconds == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", seconds)
}

func trackLabel(track db.ImportTags) string {
	return fmt.Sprintf("%s %q", position(track.DiscNumber, track.TrackNumber), track.Title)
}

func joinLabels(tracks []db.ImportTags, indexes []int) string {
	labels := make([]string, 0, len(indexes))
	for _, index := range indexes {
		labels = append(labels, trackLabel(tracks[index]))
	}
	return joinWords(labels)
}

func joinWords(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}
