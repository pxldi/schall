package identity

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

// Resolving an independently acquired file is the mirror image of validating a
// download. A download starts from a release the user asked for and asks which
// track each file is; a file someone bought, ripped, or copied into a watched
// folder starts from nothing and asks what it is at all.
//
// A playlist entry asks the identical question one step earlier, before any file
// exists: this artist, this title, this duration, sometimes this ISRC — which
// recording is that? Both questions are answered here, by the same rules, on
// purpose. Two graders would eventually disagree about what counts as
// agreement, and the looser of them would decide what reaches the library.
//
// The candidate set is therefore the whole provider database rather than one
// edition, which makes coincidence much cheaper: two three-minute songs sharing
// a title are not rare. The rule stays the one the rest of Schall uses — an
// identity is accepted only when the evidence identifies exactly one recording
// and nothing contradicts it — but the weakest evidence that may stand on its
// own is stricter here. Title and duration settle a track within a release;
// across the whole database they need the artist as well.
//
// Everything short of that is a question, and a question is worth asking only
// when it can be answered, so what is stored is the candidates and what stands
// for and against each of them.
//
// One kind of evidence is not the music's own account of itself: what the audio
// was identified as. It is graded here with the rest, because a file a stranger
// sent can be admitted on nothing else (ADR 0002) and two graders would
// eventually disagree about that too.

// DurationToleranceMS is how far two durations may sit apart and still be read
// as the same recording. It absorbs encoder and tagger rounding, not the
// difference between an album version and a live one.
//
// Exported so a comparison made outside this package — the library witness
// comparison in internal/downloads, which measures a copy's decoded length
// against a settled file's before either package decides whether the two
// audio agree — uses the same number rather than a second one that could
// drift from this one.
const DurationToleranceMS = 5000

// maxCandidates caps how many recordings one unresolved file is described by.
// Review needs the plausible answers, not the provider's whole result page.
const maxCandidates = 5

// The states a file's external identity can be in. They are the states the
// requirement names, and each is a different question.
const (
	// StatusPending means nothing has been asked yet.
	StatusPending = "pending"
	// StatusResolved means one external identity is proven for this file.
	StatusResolved = "resolved"
	// StatusNeedsReview means plausible identities exist, none conclusive.
	StatusNeedsReview = "needs_review"
	// StatusConflict means the file's own evidence disagrees with what was
	// found, which is a different problem from having too many answers.
	StatusConflict = "conflict"
	// StatusLocalOnly means the providers know of no such recording. The file
	// is owned music all the same.
	StatusLocalOnly = "local_only"
	// StatusFailed means the provider could not be asked. It is about Schall,
	// never about the music.
	StatusFailed = "failed"
	// StatusSource means the file is named by an address on another service
	// instead of by a recording. Somebody pasted that address and confirmed it.
	// Nothing here ever writes this state: resolution asks MusicBrainz, and
	// this is the answer no question to MusicBrainz produces.
	StatusSource = "source"
)

// The two things evidence can be about. They are the noun every sentence below
// uses, because a user reading why something could not be identified has to be
// told what it is that could not be identified.
const (
	SubjectFile  = "file"
	SubjectEntry = "entry"
)

// Evidence is what one piece of music says about itself — a file through its
// tags, or an entry through the words somebody asked for it in. Nothing here
// knows which: the same evidence resolves the same way either way.
type Evidence struct {
	// Subject names what this evidence describes. An empty value reads as a
	// file, which is what evidence was until entries existed.
	Subject     string
	RecordingID uuid.UUID
	ISRC        string
	Artist      string
	Album       string
	Title       string
	DurationMS  int
	// Acoustic is what the audio turned out to be, when somebody listened to it.
	// Nil means nobody did, and that is silence rather than a verdict — an entry
	// has no audio at all, and most files are never fingerprinted.
	Acoustic *Acoustic
	// Anchor is this audio measured against a piece of audio whose identity is
	// already known. Nil means no such measurement was made, which is silence:
	// most music has no anchor, an entry has no audio to measure, and a
	// comparison that could not run has not answered anything.
	Anchor *AnchorComparison
	// Explicit is the source saying this entry is the explicit recording of the
	// track. It only excludes: an entry flagged explicit is never resolved to a
	// row MusicBrainz marks as the clean edition, and the flag admits nothing on
	// its own (ADR 0032). False is silence, which is what a file and
	// every source but Spotify carry.
	Explicit bool
	// Witness is this audio measured against a file the library already holds
	// whose identity is settled. Nil means no such comparison was made, which is
	// silence: the library may hold no settled copy of this recording, the file
	// may have no fingerprint kept, and a comparison that could not run has
	// answered nothing.
	Witness *WitnessComparison
}

// WitnessComparison is how far this audio sits from a file the library already
// holds and has already proven.
//
// The known audio is an owned file whose identity was settled by a proof — a
// person's decision, an AcoustID identification, an ISRC or a MusicBrainz
// recording ID. So the library is the reference here, and it needs no service,
// no request and no catalogue lookup to be read: the fingerprint is already on
// record for the file, and the copy is fingerprinted anyway.
//
// It carries the number and not a conclusion, for the same reason
// AnchorComparison does: what the number means is decided in one place, by
// chromaprint's measured thresholds.
//
// One half of that reading is deliberately dropped. A distance small enough
// proves the two are one recording; a large distance proves nothing at all,
// because two masters of one recording — a remaster, a different pressing — are
// still that recording, and only the distributor's own excerpt or AcoustID may
// say a copy is other music. So this can admit and it can never refuse.
//
// RecordingID is what the library file was proven to be, and it is what makes
// the number evidence about a recording rather than about audio in general.
//
// FileDurationMS and CandidateDurationMS are both decoded lengths, never a
// tag: the settled file's own duration and the copy's, as fpcalc measured it
// while reading the audio the fingerprint was taken from. BitErrorRateFromStart
// reads only the span the two fingerprints share, so a low rate proves nothing
// by itself about a copy many times the settled file's length — an hour-long
// mix can share a two-minute prefix with the wanted recording and measure as
// close as a true copy over that shared span. The two lengths are carried so
// this can be told apart from a real match: see compareWitness. Zero means the
// length is not known, which never agrees with anything.
type WitnessComparison struct {
	RecordingID              uuid.UUID
	Rate                     float64
	FileDurationMS           int
	CandidateDurationMS      int
	Comparison               *chromaprint.Comparison
	ReferenceCoverageSeconds int
	CandidateCoverageSeconds int
}

// AnchorComparison is how far this audio sits from audio that is known to be the
// wanted recording.
//
// The known audio is a distributor's excerpt or an upload selected by name. A
// keyed excerpt needs no catalogue entry and no fingerprint database to have
// heard the music. The comparison is the bit error rate at the best alignment of
// the two fingerprints: how much of the excerpt the copy fails to reproduce. A
// copy that holds the excerpt's audio scores near zero whatever
// it was re-encoded through, and a copy of other music scores near half.
//
// It carries the number and not a conclusion. What the number means is decided
// in one place, by chromaprint's calibrated thresholds
// (ADR 0024), so no
// caller can set its own bar.
//
// RecordingID is the recording the excerpt was published for, and it is what
// makes the number evidence about a recording rather than about audio in
// general. The excerpt says "this is that piece of music"; without the name of
// the piece, one comparison would speak for every recording it was set beside —
// and a file weighed against a list of candidates would come back proven for all
// of them at once.
type AnchorComparison struct {
	RecordingID              uuid.UUID
	Rate                     float64
	Comparison               *chromaprint.Comparison
	ReferenceCoverageSeconds int
	CandidateCoverageSeconds int
	CandidateDurationMS      int
	// Source names who published the excerpt and how it was found. It decides two
	// things: whether this evidence may refuse a copy (see ReadAnchor), and
	// whether it may stand as an identification where the credit disagrees (see
	// AnchorSourceDeezer). An empty source is read as the weaker kind on both
	// counts.
	Source string
}

// AnchorSourceDeezer names the one anchor keyed by the recording's own ISRC:
// the excerpt Deezer publishes, fetched by that ISRC (internal/anchor,
// ADR 0024). An anchor from anywhere else was found some other way,
// and it does not stand in for an identification where the credit disagrees.
const AnchorSourceDeezer = "deezer"

// AnchorByName is the anchor source whose reference was found by searching for
// the want's name rather than fetched by a key. internal/anchor writes it and
// compareAnchor reads it. It remains a question even when the audio agrees.
//
// A reference nobody keyed can be a sibling version of the song with the same
// name and the same length, so it never proves a copy and it never refuses one:
// a wrong reference must not make the right copy unfillable (ADR 0029
// §3). A Topic upload is the exception because YouTube generates that channel
// from the distributor's delivery for the artist's recording.
const AnchorByName = "youtube"

// AnchorByNameTopic is the source for an artist-matching YouTube Topic upload.
const AnchorByNameTopic = "youtube-topic"

// Acoustic is the one thing a file does not claim about itself. Every tag is the
// file's own account of what it is; this is what it sounds like, which is why it
// is the only evidence that may admit a file a stranger sent (ADR 0002).
//
// Clusters is AcoustID's answer with its own boundaries left in, best first, and
// the boundaries are the whole point. Within one cluster AcoustID is saying these
// recordings are the same piece of audio, which is ordinary: MusicBrainz holds one
// performance as several rows all the time, the same take credited to the remixer
// on a compilation, or entered twice with one row carrying no release. Between two
// clusters it says nothing of the kind — the second is other audio that resembled
// the fingerprint, and its recordings have no more to do with this file than a
// stranger's.
//
// So the answer is read at the leading cluster and nowhere else, for agreement and
// for contradiction alike. Anything below it is not weaker evidence; it is not
// evidence about this file at all.
//
// An empty answer is an answer about the database rather than about the file:
// AcoustID has never heard most self-released music. It refuses nothing.
type Acoustic struct {
	Clusters []AcousticCluster
}

// AcousticCluster is one fingerprint cluster: the recordings AcoustID linked to
// it, and how close it thought the audio was.
//
// Similarity is read in exactly one place — whether the identification is allowed
// to admit a file — and never anywhere else. See acousticFloor.
type AcousticCluster struct {
	// ID is AcoustID's identifier for the result that answered the lookup.
	ID         string
	Recordings []uuid.UUID
	Similarity float64
}

// best is the cluster the answer is read at, which is the one AcoustID put first.
//
// The service ranks its own results, so first is best-scoring, and taking it as
// given rather than re-sorting keeps two things true: the number the likeness
// floor is applied to is exactly the number it was applied to before clusters
// were kept apart, and there is no tie between equal scores for Schall to break —
// breaking one would be choosing between two pieces of audio by coin toss.
func (acoustic *Acoustic) best() *AcousticCluster {
	if acoustic == nil || len(acoustic.Clusters) == 0 {
		return nil
	}
	return &acoustic.Clusters[0]
}

func (evidence Evidence) subject() string {
	if evidence.Subject == "" {
		return SubjectFile
	}
	return evidence.Subject
}

// searchable reports whether the evidence offers anything to ask a provider
// about. A path is not evidence: naming a file after a song does not make it
// one.
//
// The audio counts because it is the one thing here that is not written on the
// file. A recording with nothing in its tags at all says nothing a provider can
// be asked about, and until somebody listened to it there was no other question
// to put — which is how a file with no tags came to be unaskable rather than
// merely unidentified.
func (evidence Evidence) searchable() bool {
	return evidence.RecordingID != uuid.Nil ||
		strings.TrimSpace(evidence.ISRC) != "" ||
		strings.TrimSpace(evidence.Title) != "" ||
		evidence.namedByAudio()
}

// namedByAudio reports whether the audio named any recording at all, read at the
// leading cluster and nowhere else — the same reading compareAcoustic makes, for
// the same reason. Names in a lower cluster are other audio that resembled the
// fingerprint, so a file whose leading cluster is empty has not been named by
// anything, however much sits underneath it.
func (evidence Evidence) namedByAudio() bool {
	best := evidence.Acoustic.best()
	if best == nil {
		return false
	}
	for _, recordingID := range best.Recordings {
		if recordingID != uuid.Nil {
			return true
		}
	}
	return false
}

// Identity is a proven external identity for one file.
type Identity struct {
	RecordingID    uuid.UUID
	ReleaseGroupID uuid.UUID
	ArtistName     string
	ReleaseTitle   string
	TrackTitle     string
	DurationMS     *int
	ISRC           string
	Method         string
	Confidence     float32
	Summary        string
	// Evidence names what agreed, so a stored identity can be read back as the
	// reason it was accepted.
	Evidence []string
}

// Candidate is one recording a file might be, together with what stands for and
// against it.
type Candidate struct {
	RecordingID    uuid.UUID
	ReleaseGroupID uuid.UUID
	ArtistName     string
	ReleaseTitle   string
	TrackTitle     string
	DurationMS     *int
	ISRC           string
	Rank           int
	Agrees         []string
	Differs        []string
	Summary        string
}

// Outcome is everything one resolution attempt concluded.
type Outcome struct {
	Status     string
	Summary    string
	Identity   *Identity
	Candidates []Candidate
}

// comparison is everything one local file and one provider recording have to
// say to each other.
type comparison struct {
	recording tagmatch.Verdict
	isrc      tagmatch.Verdict
	artist    tagmatch.Verdict
	release   tagmatch.Verdict
	title     tagmatch.Verdict
	duration  tagmatch.Verdict
	// acoustic is what the audio said about this recording, read at the leading
	// cluster alone, and acousticNames is how many recordings that one cluster
	// named. Several names in one cluster is AcoustID saying MusicBrainz has this
	// audio entered more than once, not a question about which audio it is.
	acoustic      tagmatch.Verdict
	acousticNames int
	// acousticSimilarity is how close AcoustID thought that cluster was. It gates
	// admission by audio and nothing else — see acousticFloor.
	acousticSimilarity float64
	// anchor is what comparing this audio against this recording's own published
	// excerpt came to. It is audio evidence: it can prove the pair and it can void
	// it, and it is the only evidence here that neither MusicBrainz nor AcoustID
	// had to have heard the music to give.
	anchor tagmatch.Verdict
	// anchorReproduced says the copy reproduced the excerpt even where anchor is
	// Unknown because the excerpt was found by name (AnchorByName). The grader
	// concludes nothing from it; the row a person reads still says the audio
	// matched, because that is the fact they are being asked to weigh.
	anchorReproduced bool
	// anchorSource says how the excerpt was found. It distinguishes an excerpt
	// keyed by this recording's ISRC from one found under its name, and the row a
	// person reads says what the copy was compared against.
	anchorSource string
	// witness is what comparing this audio against a settled library file came
	// to. It is audio evidence and it can prove the pair; unlike every other kind
	// here it can never void one, because a copy that is a different master of
	// this recording is still this recording.
	witness tagmatch.Verdict

	durationDeltaMS int
}

// identifiedAcoustically reports audio that named this recording closely enough
// to be read as an identification at all. Below the floor the audio is not
// weaker evidence for this recording — it is not evidence of identity, and the
// file falls to whatever else stands, which for a copy a stranger sent is
// nothing that may admit it (ADR 0002).
//
// The floor gates admission only. Audio that named some other recording is a
// contradiction at any similarity: see contradicted.
func (pair comparison) identifiedAcoustically() bool {
	return pair.acoustic == tagmatch.Agrees && pair.acousticSimilarity >= acousticFloor
}

// contradicted reports evidence that argues these are different recordings. A
// release that disagrees is ordinary — the same recording is collected on many
// of them — and never a contradiction on its own.
//
// Audio that was recognised and does not name this recording is the file saying
// what it is, and that outranks every tag on it.
//
// Audio that fails to reproduce the recording's own published excerpt says the
// same thing by a different route, and it says it whatever else agrees: a
// contradiction voids a pair however good the rest of it looked
// (ADR 0024, rule 3).
func (pair comparison) contradicted() bool {
	return pair.recording == tagmatch.Differs || pair.isrc == tagmatch.Differs ||
		pair.creditContradicts() || pair.duration == tagmatch.Differs ||
		pair.acoustic == tagmatch.Differs || pair.anchor == tagmatch.Differs
}

// creditContradicts reports an artist credit that voids the pair.
//
// A credit that disagrees is ordinarily the file saying it is other music: a
// cover or a re-recording, which a matching title and duration cannot tell from
// the original, and which the tagging pass would overwrite after a match so that
// the mistake could not be read back afterwards.
//
// It says nothing of the kind once the audio has already named the recording.
// AcoustID's leading cluster and the excerpt this recording's own distributor
// published are both somebody else's account of what the audio is, and a credit
// typed into a file by whoever encoded it does not overrule them. The difference
// stays on the record either way: the verdict goes on listing the artist as
// disagreeing, so the copy is admitted with its disagreement in front of anybody
// reading it (ADR 0030).
//
// Those two witnesses and no others. A library witness is one copy of the music
// rather than anybody who published it, and it never refuses anything, so it is
// not asked to clear a refusal either. Neither is an excerpt found under this
// recording's name instead of by its ISRC (ADR 0029).
func (pair comparison) creditContradicts() bool {
	return pair.artist == tagmatch.Differs &&
		!pair.identifiedAcoustically() && !pair.identifiedBySample()
}

// identifiedBySample reports the distributor's own excerpt naming this
// recording. It is read only where the excerpt was fetched by the recording's
// ISRC, which is what makes it evidence about this recording rather than about
// audio published under its name.
func (pair comparison) identifiedBySample() bool {
	return pair.anchor == tagmatch.Agrees && pair.anchorSource == AnchorSourceDeezer
}

// linked reports whether anything ties this evidence to this recording at all —
// whether the recording is worth showing as a possible answer.
//
// It deliberately admits a pair whose title disagrees while the artist and the
// duration agree, and that is the streaming-metadata case: "Glory Box -
// Remastered 2011" is not the title of any MusicBrainz recording, so a decorated
// title with no ISRC would otherwise make the recording invisible and the user
// would be told nothing was found for a song plainly sitting in the database. A
// queue with nothing in it is worse than a queue with a question in it.
//
// This widens what is asked and nothing else. gradeOf still requires the title
// to agree before artist and duration may conclude anything, so such a pair can
// only ever become an answer by a person choosing it with both the agreements
// and the disagreements in front of them.
//
// Audio that named this recording admits it here for the same reason. A cluster
// too weak to identify anything, and a cluster naming two rows MusicBrainz keeps
// for one performance, are both short of an answer — and both are exactly what
// somebody has to be shown in order to give one.
func (pair comparison) linked() bool {
	return pair.recording == tagmatch.Agrees || pair.isrc == tagmatch.Agrees ||
		pair.title == tagmatch.Agrees || pair.acoustic == tagmatch.Agrees ||
		pair.anchor == tagmatch.Agrees || pair.witness == tagmatch.Agrees ||
		(pair.artist == tagmatch.Agrees && pair.duration == tagmatch.Agrees)
}

// identifies reports whether an identifier of the file names this recording.
// It is what separates a file whose identifiers were contradicted — a conflict
// worth showing as such — from one that simply has several plausible answers.
func (pair comparison) identifies() bool {
	return pair.recording == tagmatch.Agrees || pair.isrc == tagmatch.Agrees
}

type grade int

const (
	noMatch grade = iota
	candidateOnly
	byTitleArtistDuration
	byISRC
	byRecordingID
	// bySample is the audio reproducing the excerpt this recording's distributor
	// publishes. It sits above the identifiers because it is the music itself
	// rather than what somebody wrote about the music, which is the same reason
	// an AcoustID identification sits above them.
	//
	// It is not ranked against AcoustID here in any meaningful sense. gradeOf
	// reads the AcoustID cases first, so a copy both witnesses agree about is
	// recorded under the name it would have carried before excerpts existed, and
	// this order decides nothing else: a want is checked against exactly one
	// recording, so there is no second candidate for it to win against.
	bySample
	// byLibraryWitness is the audio agreeing with a file the library already
	// holds and has already proven to be this recording. It sits beside the
	// excerpt for the same reason: it is the music itself rather than what
	// somebody wrote about the music.
	//
	// What it rests on is the proof that settled the older file, which is why an
	// identity recorded this way keeps a note of which file admitted it and what
	// proved that one. Withdraw the older proof and this one goes with it.
	byLibraryWitness
	byFingerprintAndIdentifier
	byFingerprint
)

const weakestConclusive = byTitleArtistDuration

// acousticFloor is how close AcoustID has to think the audio was before an
// identification may admit a file.
//
// It exists because AcoustID decides for itself what reaches an answer at all,
// so a lookup returning one weak cluster arrives here indistinguishable from a
// certain one: a cluster, and nothing to say how sure anybody was. Concluding on
// that borrows whatever cutoff AcoustID applied before answering — a threshold
// Schall does not set, cannot see and could not review. This is that threshold
// made its own and visible, applied to the one cluster the answer is read at.
//
// It is not a confidence rule and cannot become one. It only ever moves an
// answer from identified to undecided: a file below it is held with its
// evidence, which is the same question a file nothing could decide about
// already becomes. Nothing is admitted because of it, no ambiguity is resolved
// by it, and it never orders candidates. A refusal is likewise untouched —
// audio that names some other recording still voids the pair at any similarity,
// because that is a statement about what the music is and lowering the bar for
// believing it would be the one direction that could let a wrong file in.
//
// 0.90 is below every similarity the deployment has ever recorded (the lowest
// was 0.99 across 17 lookups, 2026-07-30), so it changes no decision made so
// far. It is a guard against the case that has not arrived, set where it costs
// nothing until it does.
const acousticFloor = 0.90

func (level grade) conclusive() bool { return level >= weakestConclusive }

func (level grade) method() string {
	switch level {
	case byFingerprint:
		return "fingerprint"
	case byFingerprintAndIdentifier:
		return "fingerprint-and-identifier"
	case bySample:
		return "published-sample"
	case byLibraryWitness:
		return "library-witness"
	case byRecordingID:
		return "recording-id"
	case byISRC:
		return "isrc"
	case byTitleArtistDuration:
		return "title-artist-duration"
	}
	return ""
}

// singledOutByRelease is the ending resolvedOutcome puts on a method when
// several recordings were all proven and the release named one of them.
const singledOutByRelease = "-and-release"

// singledOutByExplicitEdition is the token resolvedOutcome appends when several
// recordings were all proven and every one but this was the clean edition of it
// (ADR 0032). The method carries it as well as the sentence, because
// "the one recording the tags name" and "one of two, taken because MusicBrainz
// marks the other as clean" are different things to have concluded.
const singledOutByExplicitEdition = "explicit-edition"

// MethodSameRegistration is what a file re-filed under a want's recording
// carries. No grade produces it: it is written where MusicBrainz merged the row
// the library used into the want's, or where something proved the copy is the
// want's recording and the row the library used is the same registered track
// (ADR 0025, 0031). Which of the two it was, and what proved the copy,
// are on the identity's evidence rather than in this name.
const MethodSameRegistration = "same-registration"

// MethodSentence says, in plain words, what identified a file. Every resolved
// identity carries a method: a short name Schall uses to itself, such as
// "recording-id" or "fingerprint-and-identifier". Those names are for programs.
// A person reading why a file was identified has to be told what the evidence
// was, so this is the sentence the interface shows and the name stays in the
// answer beside it.
//
// Listed here is every name method() gives, each one on its own and with the
// ending resolvedOutcome adds when the release chose between recordings that
// fit equally well. A name from an older version, or a name written by hand,
// still comes back as a sentence: an internal name must never reach the screen.
//
// Nothing here decides anything. The evidence was weighed when the identity was
// recorded; this reads that decision back.
func MethodSentence(method string) string {
	singledOut := ""
	base, cut := strings.CutSuffix(method, singledOutByRelease)
	if cut {
		singledOut = " More than one recording fit, and the file's album tag names this one."
	}
	if trimmed, cut := strings.CutSuffix(base, "-and-"+singledOutByExplicitEdition); cut {
		base = trimmed
		singledOut = " More than one recording fit, and MusicBrainz marks the other one " +
			"as the clean edition of this."
	}
	sentence := ""
	switch base {
	case "recording-id":
		sentence = "The file's tags carry this recording's MusicBrainz identifier."
	case "isrc":
		sentence = "The file's tags carry this recording's ISRC, the code a record label gives a track."
	case "fingerprint":
		sentence = "Schall listened to the audio and it matches this recording."
	case "published-sample":
		sentence = "Schall compared the audio with the sample this recording's " +
			"distributor publishes, and it is the same music."
	case "library-witness":
		sentence = "Schall compared the audio with a file already in your library " +
			"that is proven to be this recording, and it is the same music."
	case "fingerprint-and-identifier":
		sentence = "Schall listened to the audio and it matches this recording. " +
			"The file's tags name the same one."
	case "title-artist-duration":
		sentence = "The title, artist and length in the file's tags all match this recording."
	case MethodSameRegistration:
		sentence = "Schall proved this file is this recording. Your library had " +
			"filed it under a second MusicBrainz entry for the same track."
	case "manual":
		sentence = "Somebody chose this recording by hand."
	case "source_url":
		sentence = "Somebody gave the address of the track this file holds."
	}
	if sentence == "" {
		return "Schall settled this automatically from the evidence it had."
	}
	return sentence + singledOut
}

// ProvenForItsWant reports that the record of how a copy was admitted says the
// copy is the recording its want names, on grounds outside the catalogue's
// bookkeeping.
//
// Two things qualify. The audio answered — AcoustID at the leading cluster, the
// excerpt a distributor publishes for this recording, or a file the library
// already holds and has proven — or a person listened and said so. Nothing else
// does: a tag method is what the file says about itself, which never admits a
// copy a stranger sent (ADR 0002), and an empty method is a copy
// nothing is recorded about.
//
// It is Verification.Audio read back off a method written down earlier, for a
// caller holding the record of a decision rather than the decision itself. The
// endings resolvedOutcome adds come off first, because a recording singled out
// by its release was still proven by whatever proved it.
func ProvenForItsWant(method string) bool {
	base, _ := strings.CutSuffix(method, singledOutByRelease)
	if trimmed, cut := strings.CutSuffix(base, "-and-"+singledOutByExplicitEdition); cut {
		base = trimmed
	}
	switch base {
	case "fingerprint", "fingerprint-and-identifier", "published-sample",
		"library-witness", "manual":
		return true
	}
	return false
}

func (level grade) confidence() float32 {
	switch level {
	case byFingerprint, byFingerprintAndIdentifier:
		return 1
	case bySample, byLibraryWitness:
		return 1
	case byRecordingID:
		return 1
	case byISRC:
		return 0.98
	case byTitleArtistDuration:
		return 0.9
	}
	return 0
}

func (level grade) summary() string {
	switch level {
	case byFingerprint:
		return "identified by its audio"
	case byFingerprintAndIdentifier:
		return "identified by its audio together with its own identifier"
	case bySample:
		return "identified by the sample its distributor publishes"
	case byLibraryWitness:
		return "identified by a file your library already holds"
	case byRecordingID:
		return "identified by MusicBrainz recording ID"
	case byISRC:
		return "identified by ISRC"
	case byTitleArtistDuration:
		return "identified by artist, title, and duration together"
	}
	return ""
}

// gradeOf decides how much one comparison is worth. Nothing is conclusive while
// any evidence contradicts it, including a contradiction of an identifier: a
// file whose recording ID agrees but whose duration is a minute out is
// mistagged, not identified, and that is a question for a person.
func gradeOf(pair comparison) grade {
	if pair.contradicted() {
		return candidateGrade(pair)
	}
	switch {
	// The audio is the only evidence that is not the file's own account of itself,
	// and the leading cluster naming this recording is the strongest thing Schall
	// can know about a file — however many recordings that one cluster carries,
	// because a cluster is AcoustID's own statement that they are one piece of
	// audio. The file's own identifier picking the same recording out of it adds a
	// second, independent witness, so it is recorded as the stronger thing it is
	// rather than folded into the same word.
	case pair.identifiedAcoustically() && pair.identifies():
		return byFingerprintAndIdentifier
	case pair.identifiedAcoustically():
		return byFingerprint
	// The audio again, from the anchor. A matching distributor preview or Topic
	// upload is audio evidence for this recording, so it can prove a copy that
	// tags, MusicBrainz and AcoustID do not identify.
	//
	// It is read after the AcoustID cases so that a copy both witnesses agree
	// about keeps the name it always had.
	case pair.anchor == tagmatch.Agrees:
		return bySample
	// The audio a third time, and this witness lives at home. The copy holds the
	// same music as a file the library already has and has already proven to be
	// this recording, so what proved that file proves this copy too.
	//
	// It is read after both of the others so that a copy several witnesses agree
	// about keeps the name it would have carried without this one.
	case pair.witness == tagmatch.Agrees:
		return byLibraryWitness
	case pair.recording == tagmatch.Agrees:
		return byRecordingID
	case pair.isrc == tagmatch.Agrees:
		return byISRC
	case pair.title == tagmatch.Agrees && pair.artist == tagmatch.Agrees &&
		pair.duration == tagmatch.Agrees:
		return byTitleArtistDuration
	}
	return candidateGrade(pair)
}

func candidateGrade(pair comparison) grade {
	if pair.linked() {
		return candidateOnly
	}
	return noMatch
}

// compare reads one local file against one provider recording. The recording's
// releases are compared as a set, because a recording collected on five
// editions disagrees with a file's album tag only if none of them is that album.
func compare(evidence Evidence, recording musicbrainz.Recording) comparison {
	return compareAgainst(evidence, recording, nil)
}

// compareAgainst is compare with the other MusicBrainz rows that hold this same
// registered track named as such.
//
// sameRegistration is empty everywhere except one path. MusicBrainz sometimes
// enters one performance as two recordings and never merges them, and then a
// fingerprint names the row the want is not keyed to. Verify establishes when
// that has happened, by a proof rather than by resemblance, and the names it
// establishes are passed here so that the audio is not read as naming other
// music. See secondRowOfSameRegistration and ADR 0025.
func compareAgainst(
	evidence Evidence, recording musicbrainz.Recording, sameRegistration []uuid.UUID,
) comparison {
	pair := comparison{
		recording: compareRecordingID(evidence.RecordingID, recording),
		isrc:      compareISRC(evidence.ISRC, recording.ISRCs),
		artist:    compareCredit(evidence.Artist, recording),
		title:     tagmatch.Title(evidence.Title, recording.Title),
		release:   compareRelease(evidence.Album, recording.Releases),
	}
	if evidence.DurationMS > 0 && recording.DurationMS != nil && *recording.DurationMS > 0 {
		pair.durationDeltaMS = abs(evidence.DurationMS - *recording.DurationMS)
		pair.duration = tagmatch.Agrees
		if pair.durationDeltaMS > DurationToleranceMS {
			pair.duration = tagmatch.Differs
		}
	}
	pair.acoustic, pair.acousticNames = compareAcoustic(
		evidence.Acoustic, recording, sameRegistration)
	if best := evidence.Acoustic.best(); best != nil {
		pair.acousticSimilarity = best.Similarity
	}
	pair.anchor = compareAnchor(evidence.Anchor, recording)
	pair.anchorReproduced = pair.anchor == tagmatch.Agrees
	if evidence.Anchor != nil && evidence.Anchor.Source == AnchorByName {
		// Read once more as if the upload were the artist's own, so the same
		// window and coverage checks decide whether "matched" may be said.
		asTopic := *evidence.Anchor
		asTopic.Source = AnchorByNameTopic
		pair.anchorReproduced = compareAnchor(&asTopic, recording) == tagmatch.Agrees
	}
	if evidence.Anchor != nil {
		pair.anchorSource = evidence.Anchor.Source
	}
	pair.witness = compareWitness(evidence.Witness, recording)
	return pair
}

// compareCredit reads the file's artist tag against the artists the recording
// credits.
//
// MusicBrainz sends the credit as the list of artists it is, and where it did
// the comparison is made over that set (ADR 0030). A recording that
// arrived without the list falls back to the printed credit, which is the
// comparison this always made.
func compareCredit(observed string, recording musicbrainz.Recording) tagmatch.Verdict {
	if credited := creditedArtists(recording); len(credited) > 0 {
		return tagmatch.Artists(observed, credited)
	}
	return tagmatch.Credit(observed, recording.ArtistCredit)
}

// creditedArtists is the wanted side of that comparison: every artist the
// recording credits, and every name each of them answers to. That is the name
// this release prints, the artist's own name, and the aliases MusicBrainz
// records for them. "Travi$ Scott" and Travis Scott are one artist because the
// provider says so, and for no other reason.
//
// One artist credited twice is folded into one entry. The question is which
// artists the credit names, and a credit that names one of them twice still
// names one artist.
func creditedArtists(recording musicbrainz.Recording) []tagmatch.CreditedArtist {
	credited := make([]tagmatch.CreditedArtist, 0, len(recording.Credits))
	seen := make(map[string]bool, len(recording.Credits))
	for _, credit := range recording.Credits {
		key := credit.ArtistID.String()
		if credit.ArtistID == uuid.Nil {
			key = tagmatch.Text(credit.Name)
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		names := make([]string, 0, len(credit.Aliases)+2)
		names = append(names, credit.Name, credit.ArtistName)
		artist := tagmatch.CreditedArtist{Names: append(names, credit.Aliases...)}
		if credit.ArtistID != uuid.Nil {
			artist.ID = credit.ArtistID.String()
		}
		credited = append(credited, artist)
	}
	return credited
}

// compareWitness reads the measured distance from a file the library already
// holds, against the recording in hand.
//
// The library file names its own recording: its identity was settled by a proof
// and the comparison is only ever made against files carrying that proof. So the
// number answers for that recording and for no other, and it is read only where
// the two are the same recording — the shape compareAnchor has, for the same
// reason. Merged identifiers still answer, because Answers covers the
// identifiers a recording was merged away from.
//
// Only agreement is read. Above the discard threshold this says nothing at all
// rather than saying the copy is other music: two masters of one recording
// measure far apart and both are that recording, and the witnesses entitled to
// call a copy other music are the ones that were told what the recording is by
// somebody who published it — AcoustID's database and the distributor's own
// excerpt. A library file is not that; it is one copy of the music.
func compareWitness(witness *WitnessComparison, recording musicbrainz.Recording) tagmatch.Verdict {
	if witness == nil || witness.RecordingID == uuid.Nil ||
		!recording.Answers(witness.RecordingID) {
		return tagmatch.Unknown
	}
	// The rate alone is not enough. It is read only over the span the two
	// fingerprints share, so a copy that hit the length limit while decoding, or
	// one many times longer than the settled file, can measure as close as a
	// true copy over its opening span and still be a stranger past it — an
	// hour-long mix that starts with the wanted recording is the concrete case.
	// Both lengths have to be known, from a decode and never a tag, and they
	// have to agree within the same tolerance a duration tag would.
	if witness.CandidateDurationMS <= 0 || witness.FileDurationMS <= 0 {
		return tagmatch.Unknown
	}
	if abs(witness.CandidateDurationMS-witness.FileDurationMS) > DurationToleranceMS {
		return tagmatch.Unknown
	}
	if witness.Comparison != nil {
		if !witness.Comparison.WindowsAgree() ||
			!comparisonCovers(witness.ReferenceCoverageSeconds, witness.FileDurationMS) ||
			!comparisonCovers(witness.CandidateCoverageSeconds, witness.CandidateDurationMS) {
			return tagmatch.Unknown
		}
	}
	if chromaprint.Read(witness.Rate) == chromaprint.SameAudio {
		return tagmatch.Agrees
	}
	return tagmatch.Unknown
}

func comparisonCovers(coverageSeconds, durationMS int) bool {
	return coverageSeconds > 0 && durationMS > 0 &&
		coverageSeconds*1000+DurationToleranceMS >= durationMS
}

// compareAnchor reads the measured distance from a published excerpt, against
// the recording in hand.
//
// The excerpt names its own recording, because it was fetched by that
// recording's ISRC. So the comparison answers for that recording and for no
// other, and it is read only where the two are the same recording — the same
// shape compareAcoustic has, for the same reason. An excerpt of something else
// is not weaker evidence about this recording; it is evidence about a different
// one, and reading it here would let one measurement prove every candidate it
// was set beside at once.
//
// An excerpt read against merged identifiers still answers: Answers covers the
// identifiers the recording was merged away from, so a want resolved before a
// merge is not silently unanchored by one.
//
// The thresholds are chromaprint's, measured rather than chosen, and the middle
// band is a question for a person rather than a weaker yes.
func compareAnchor(anchor *AnchorComparison, recording musicbrainz.Recording) tagmatch.Verdict {
	if anchor == nil || anchor.RecordingID == uuid.Nil ||
		!recording.Answers(anchor.RecordingID) {
		return tagmatch.Unknown
	}
	return readAnchorComparison(anchor)
}

// readAnchorComparison is what one measured anchor comparison says, with the
// window and coverage checks applied to an agreement. It does not ask which
// recording the anchor names. compareAnchor asks that first, and the
// anchor-only check has no recording to ask about (ADR 0037).
func readAnchorComparison(anchor *AnchorComparison) tagmatch.Verdict {
	verdict := ReadAnchor(anchor.Source, anchor.Rate)
	if verdict == tagmatch.Agrees && anchor.Comparison != nil {
		if !anchor.Comparison.WindowsAgree() ||
			!comparisonCovers(anchor.CandidateCoverageSeconds, anchor.CandidateDurationMS) {
			return tagmatch.Unknown
		}
	}
	return verdict
}

// ReadAnchor turns one measured distance into a verdict, given who published the
// reference. It is the whole of what an anchor means, and compareAnchor is this
// with the recording checked first — so a caller that already knows the
// measurement answers for the want's own recording cannot reach a looser answer
// than the grader does.
//
// The thresholds are chromaprint's, measured rather than chosen, and the middle
// band is a question for a person rather than a weaker yes.
func ReadAnchor(source string, rate float64) tagmatch.Verdict {
	switch chromaprint.Read(rate) {
	case chromaprint.SameAudio:
		if source == AnchorByName {
			return tagmatch.Unknown
		}
		return tagmatch.Agrees
	case chromaprint.OtherAudio:
		// Other audio refuses, except where the reference was found by name. That
		// reference may be another version of the same song, and reading it as a
		// refusal would throw the right copy away against the wrong upload, for
		// good (ADR 0029 §3).
		if source == AnchorByName || source == AnchorByNameTopic {
			return tagmatch.Unknown
		}
		return tagmatch.Differs
	default:
		return tagmatch.Unknown
	}
}

// compareRecordingID reads the file's MusicBrainz recording ID against every ID
// the recording answers to, which is its own and any it was merged away from.
// A file tagged before a merge names this recording; reading it as naming other
// music would refuse the best-tagged copy there is, and permanently, because the
// tag was right when it was written.
//
// Nothing here is loosened for a recording that was never merged: an ID the
// provider has not tied to this recording still disagrees.
func compareRecordingID(observed uuid.UUID, recording musicbrainz.Recording) tagmatch.Verdict {
	if observed == uuid.Nil {
		return tagmatch.Unknown
	}
	if recording.Answers(observed) {
		return tagmatch.Agrees
	}
	return tagmatch.Differs
}

// compareAcoustic reads what the audio was identified as against one recording,
// at the leading cluster and nowhere else. An identification that named nothing
// there is silence: either the database has not heard this music or it knows the
// audio and MusicBrainz has no row for it, and neither says anything about the
// file either way.
//
// Recordings named only by a lower cluster are not read at all. They neither
// agree nor contradict, because a lower cluster is different audio that resembled
// the fingerprint — the wanted recording turning up in one is the file being
// mistaken for the music somebody wanted, which is precisely the case that has to
// go on being refused.
//
// A named ID is read against every ID the recording answers to rather than
// against its own alone. AcoustID's database carries recording IDs submitted
// over many years, so a fingerprint can name the ID a recording was merged away
// from; read by equality that is the audio naming other music, which is the one
// refusal nothing else can outweigh.
//
// The count is of IDs in that cluster, and it is description rather than a rule:
// it says how many rows MusicBrainz keeps for this audio, which is what a
// reviewer needs told and what nothing decides on.
//
// A name in sameRegistration is set aside before anything is read. It is another
// MusicBrainz row for this same registered track, so it is neither this recording
// nor other music: the audio was identified, and what it was identified as says
// nothing about which of two rows to key an answer on. Setting it aside is
// deliberately not the same as reading it as agreement — see
// ADR 0025.
func compareAcoustic(
	acoustic *Acoustic, recording musicbrainz.Recording, sameRegistration []uuid.UUID,
) (tagmatch.Verdict, int) {
	best := acoustic.best()
	if best == nil {
		return tagmatch.Unknown, 0
	}
	named := make(map[uuid.UUID]bool, len(best.Recordings))
	setAside := false
	for _, recordingID := range best.Recordings {
		if recordingID == uuid.Nil {
			continue
		}
		if slices.Contains(sameRegistration, recordingID) {
			setAside = true
			continue
		}
		named[recordingID] = true
	}
	if len(named) == 0 {
		return tagmatch.Unknown, 0
	}
	if len(named) > 1 {
		matches, other := false, false
		for recordingID := range named {
			if recording.Answers(recordingID) {
				matches = true
			} else {
				other = true
			}
		}
		if matches && other {
			return tagmatch.Unknown, len(named)
		}
	}
	if tiedAcousticClusterConflicts(acoustic, recording) {
		return tagmatch.Unknown, len(named)
	}
	for recordingID := range named {
		if recording.Answers(recordingID) {
			return tagmatch.Agrees, len(named)
		}
	}
	if setAside {
		// The cluster names this recording under another row of the same
		// registration (ADR 0025, 0031), so whatever else it carries
		// it is not naming other music. The set-aside row admits nothing on its
		// own, so this is silence and the anchor decides.
		return tagmatch.Unknown, len(named)
	}
	return tagmatch.Differs, len(named)
}

const acousticTieEpsilon = 0.005

// tiedAcousticClusterConflicts reports whether the leading score is shared by
// clusters that name this recording and another recording. Schall cannot choose
// between those clusters from their order or their score.
func tiedAcousticClusterConflicts(acoustic *Acoustic, recording musicbrainz.Recording) bool {
	if acoustic == nil || len(acoustic.Clusters) < 2 {
		return false
	}
	leading := acoustic.Clusters[0].Similarity
	matches, other := false, false
	for _, cluster := range acoustic.Clusters {
		if leading-cluster.Similarity > acousticTieEpsilon {
			break
		}
		for _, recordingID := range cluster.Recordings {
			if recordingID == uuid.Nil {
				continue
			}
			if recording.Answers(recordingID) {
				matches = true
			} else {
				other = true
			}
		}
	}
	return matches && other
}

// compareISRC reads a file's ISRC against every ISRC the recording carries. One
// agreement is agreement; disagreement needs all of them to disagree.
func compareISRC(observed string, known []string) tagmatch.Verdict {
	if strings.TrimSpace(observed) == "" || len(known) == 0 {
		return tagmatch.Unknown
	}
	for _, candidate := range known {
		if tagmatch.Identifier(observed, candidate) == tagmatch.Agrees {
			return tagmatch.Agrees
		}
	}
	return tagmatch.Differs
}

// sharesISRC reports two recordings registered under a code they both carry. It
// is the same exact-identifier comparison compareISRC makes, asked of two
// provider recordings rather than of a file and one of them.
//
// Silence on either side is not agreement: a recording MusicBrainz lists no ISRC
// for shares nothing with anybody.
func sharesISRC(left, right []string) bool {
	for _, code := range left {
		if compareISRC(code, right) == tagmatch.Agrees {
			return true
		}
	}
	return false
}

// registrationProof is what makes two MusicBrainz rows one registered track. It
// is carried rather than reduced to a yes, because the two tests do not admit
// the same evidence afterwards: see secondRowOfSameRegistration.
type registrationProof int

const (
	notOneRegistration registrationProof = iota
	// byRegistrationCode is ADR 0025: the two rows carry an ISRC in
	// common, which is the catalogue's own statement that one track is entered
	// twice.
	byRegistrationCode
	// byNameAndLength is ADR 0031: the two rows name the same artists,
	// hold the same title and run the same length. Most of the rows MusicBrainz
	// enters twice carry no ISRC at all, and this is what says so about them.
	byNameAndLength
	// byExplicitEdition is ADR 0032: the row the want names is the
	// clean edition and the other row is the explicit one. They are two
	// registered tracks and the audio does differ, and the explicit edition is
	// the one the owner keeps, so the other row answers for the want. It holds in
	// that direction alone: a want for the explicit edition is never answered by
	// the clean one.
	byExplicitEdition
)

// rowLengthToleranceMS is how far two MusicBrainz rows may sit apart in length
// and still be one registered track.
//
// It is much tighter than the tolerance a tag gets, because both numbers are the
// catalogue's own. What it absorbs is one editor typing a length off a sleeve
// while another took it from the disc; the two rows of "Nichts hittet mehr" sit
// one millisecond apart. A speed edit sits far outside it, which is what keeps
// the edit that shares a title from reading as the original.
const rowLengthToleranceMS = 2000

// oneRegistration reports whether two MusicBrainz rows hold one registered
// track, and by which test.
//
// The name-and-length test is preferred where both hold. It is the stronger of
// the two: a shared registration code says the label registered one track, and
// MusicBrainz puts one code on genuinely different audio as well, while equal
// credits, equal titles and equal lengths leave the sped-up edit outside on
// every count.
//
// Codes that disagree end it before either test is asked, with one exception:
// where the codes are all that separates the clean edition from the explicit
// one, and the row asked about is the clean one, the explicit row answers for it
// (ADR 0032). A name and a length cannot outvote the label having
// registered the two rows separately in any other case.
//
// copyDurationMS is how long the copy in hand actually runs, and it is read only
// where MusicBrainz does not know one of the two lengths.
func oneRegistration(
	wanted, other musicbrainz.Recording, copyDurationMS int,
) registrationProof {
	switch {
	case explicitEditionOf(wanted, other, copyDurationMS):
		return byExplicitEdition
	case registrationCodesDisagree(wanted, other):
		return notOneRegistration
	case sameArtists(wanted, other) && sameTitle(wanted, other) &&
		sameLength(wanted, other, copyDurationMS):
		return byNameAndLength
	case sharesISRC(wanted.ISRCs, other.ISRCs):
		return byRegistrationCode
	}
	return notOneRegistration
}

// registrationCodesDisagree reports two rows the label registered as two tracks.
//
// A row with no ISRC is silence and disagrees with nothing, which is the usual
// shape of these pairs. Two rows that both carry codes and share none are the
// clean edit and the explicit one: MusicBrainz enters "Timeless" by The Weeknd
// twice under one title, one credit and one length of 256 seconds, with
// USUG12406537 on one row and USUG12406536 on the other, and they are different
// audio. The names and the lengths agree there and the codes are the only thing
// that separates them.
func registrationCodesDisagree(wanted, other musicbrainz.Recording) bool {
	return len(wanted.ISRCs) > 0 && len(other.ISRCs) > 0 &&
		!sharesISRC(wanted.ISRCs, other.ISRCs)
}

// sameArtists reports two credits naming the same set of artists.
//
// Where both rows say who every credited artist is, their MusicBrainz artist
// identifiers settle it and the names are not read at all. MusicBrainz renamed
// Kanye West to Ye in 2021 and left the rows entered before that printing the
// old name, so one recording entered twice can print two credits that name one
// artist. Which artist a row credits is MusicBrainz's own statement, and reading
// it is not a resemblance between two spellings.
//
// The identifiers decide in both directions. Two rows printing one name under
// two identifiers are two artists MusicBrainz spells alike, and they are two
// registrations however the names read.
//
// Otherwise the names are compared, asked in both directions. One direction is
// not enough: tagmatch.Artists answers Unknown where the tag names part of a
// longer credit, which is a fact about a file naming only the lead artist and
// not about two catalogue rows. "88GLAM" against "88GLAM & PnB Rock" has to fail
// here, and it fails on the direction the shorter credit is read from.
//
// A row that arrived without its artist list matches nothing. The comparison is
// over the set of artists, and there is no set to compare.
func sameArtists(wanted, other musicbrainz.Recording) bool {
	credited, otherCredited := creditedArtists(wanted), creditedArtists(other)
	if len(credited) == 0 || len(otherCredited) == 0 {
		return false
	}
	switch tagmatch.SameArtistIDs(credited, otherCredited) {
	case tagmatch.Agrees:
		return true
	case tagmatch.Differs:
		return false
	}
	return tagmatch.Artists(other.ArtistCredit, credited) == tagmatch.Agrees &&
		tagmatch.Artists(wanted.ArtistCredit, otherCredited) == tagmatch.Agrees
}

// sameTitle reports two titles that are the same name for the same recording,
// with the bracketed groups that describe a copy or name its producer dropped
// first. "Resin [Prod. GeeKey]" and "Resin" are one recording under two naming
// conventions; every other qualifier stays and every other qualifier disagrees.
func sameTitle(wanted, other musicbrainz.Recording) bool {
	return tagmatch.Title(
		tagmatch.WithoutBracketedLabels(wanted.Title),
		tagmatch.WithoutBracketedLabels(other.Title)) == tagmatch.Agrees
}

// sameLength reports two rows running the same length.
//
// MusicBrainz does not know every recording's length, and a row with none is
// compared through the copy instead: the copy is the audio both rows are meant
// to describe, so its own length has to sit within the tolerance a duration gets
// of every length the catalogue does know. Two rows with no length between them
// are not one registration by this test — nothing measured either of them, and
// the title and the credit alone are what a cover version also has.
func sameLength(wanted, other musicbrainz.Recording, copyDurationMS int) bool {
	known, otherKnown := lengthOf(wanted), lengthOf(other)
	if known > 0 && otherKnown > 0 {
		return abs(known-otherKnown) <= rowLengthToleranceMS
	}
	if known == 0 && otherKnown == 0 {
		return false
	}
	if copyDurationMS <= 0 {
		return false
	}
	for _, length := range []int{known, otherKnown} {
		if length > 0 && abs(copyDurationMS-length) > DurationToleranceMS {
			return false
		}
	}
	return true
}

// lengthOf is the recording's length where MusicBrainz has one, and zero where
// it has none. Zero is silence and agrees with nothing.
func lengthOf(recording musicbrainz.Recording) int {
	if recording.DurationMS == nil || *recording.DurationMS <= 0 {
		return 0
	}
	return *recording.DurationMS
}

// sameRegistrationSentence says which row the audio named and that MusicBrainz
// holds it as this same registered track.
//
// It names the row rather than its identifier. This sentence is what somebody
// reads in place of the refusal this used to be, and nobody can act on a UUID.
func sameRegistrationSentence(rows []musicbrainz.Recording) string {
	named := make([]string, 0, len(rows))
	for _, row := range rows {
		named = append(named, fmt.Sprintf("%q by %s", row.Title, credited(row.ArtistCredit)))
	}
	return fmt.Sprintf("AcoustID identified the audio as %s, which MusicBrainz holds "+
		"as another row of this same registered track.", joinWords(named))
}

// explicitEditionSentence says which row the audio named and that MusicBrainz
// marks the wanted row as the clean edition of it.
//
// It is a different sentence from sameRegistrationSentence because it is a
// different fact. These two rows are two registered tracks and the audio is not
// the same audio; what settles the copy is that the explicit edition is the one
// the owner keeps.
func explicitEditionSentence(rows []musicbrainz.Recording) string {
	named := make([]string, 0, len(rows))
	for _, row := range rows {
		named = append(named, fmt.Sprintf("%q by %s", row.Title, credited(row.ArtistCredit)))
	}
	return fmt.Sprintf("AcoustID identified the audio as %s. MusicBrainz marks the "+
		"recording this wants as the clean edition of that track, and Schall keeps "+
		"the explicit one.", joinWords(named))
}

// registrationUncheckedSentence is what is said instead when MusicBrainz could
// not be asked. A check that did not run has answered nothing, and it must read
// as nothing rather than as the audio naming other music.
const registrationUncheckedSentence = "AcoustID identified the audio as another " +
	"MusicBrainz recording, and MusicBrainz could not be asked whether that row " +
	"holds this same registered track."

func compareRelease(observed string, releases []musicbrainz.RecordingRelease) tagmatch.Verdict {
	if strings.TrimSpace(observed) == "" || len(releases) == 0 {
		return tagmatch.Unknown
	}
	for _, release := range releases {
		if tagmatch.Title(observed, release.Title) == tagmatch.Agrees {
			return tagmatch.Agrees
		}
	}
	return tagmatch.Differs
}

// graded is one provider recording with what comparing it came to.
type graded struct {
	recording musicbrainz.Recording
	pair      comparison
	level     grade
}

// namedByRelease picks the one recording the release names, out of several that
// are otherwise proof-grade.
//
// MusicBrainz catalogues one piece of music as several recordings — the single,
// the compilation appearance, the sped-up edit — and hands them the same
// identifiers: the ISRC on "1992" is the ISRC on "1992 (sped Up + slowed mixes)"
// as well. So the identifier that exists to be unique names two rows, and both
// come back proven. That is the ordinary case here, not a rare one.
//
// The release settles it, and it settles it by agreement rather than by
// ranking. It is the same exact-tag agreement the enumerated combinations are
// made of: the entry names an album, and exactly one of the recordings that fit
// appears on it. Nothing is added up and nothing is scored — a candidate does
// not win for having more ticks, it wins for being the only one the release
// picks out.
//
// Three refusals keep it honest, and each is the whole rule failing rather than
// something weaker being accepted:
//
//   - Two of them on that release, and it names neither. Ambiguous, as before.
//   - None of them on it — including an entry with no album at all, because an
//     absent tag is silence and compareRelease answers Unknown, never Agrees.
//   - Something else proven more strongly. A recording named by its own
//     MusicBrainz ID is not overturned by a weaker candidate that happens to sit
//     on the right album; the release may choose between equals and never
//     between a proof and a lesser one.
//   - Anything else it does not actually know about. compareRelease answers
//     Unknown for a recording MusicBrainz lists no releases for, and silence
//     about where a recording appears is not evidence that it does not appear
//     there — read as one it would let an unlisted recording lose to a rival
//     that merely has its editions filled in. Every other candidate has to
//     disagree, which is a release list that exists and does not contain this
//     album.
func namedByRelease(conclusive []graded) *graded {
	var named *graded
	for index, entry := range conclusive {
		if entry.pair.release != tagmatch.Agrees {
			continue
		}
		if named != nil {
			return nil
		}
		named = &conclusive[index]
	}
	if named == nil {
		return nil
	}
	for _, entry := range conclusive {
		if entry.recording.ID == named.recording.ID {
			continue
		}
		if entry.level > named.level || entry.pair.release != tagmatch.Differs {
			return nil
		}
	}
	return named
}

// withoutCleanEditions drops every row MusicBrainz marks as the clean edition
// of another row in the same list.
//
// It never picks between rows on how well they fit. A row goes only where the
// catalogue itself says it is the censored edition of a row standing beside it,
// and where the two are one track in every other respect (explicitEditionOf). A
// pair MusicBrainz marks neither way, or both ways, comes back whole and stays a
// question for a person.
//
// copyDurationMS stands in for a length MusicBrainz does not know, exactly as it
// does in oneRegistration. For an entry it is the length the source published.
func withoutCleanEditions(rows []graded, copyDurationMS int) []graded {
	kept := make([]graded, 0, len(rows))
	for _, row := range rows {
		if hasExplicitEditionAmong(row.recording, rows, copyDurationMS) {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

func hasExplicitEditionAmong(
	clean musicbrainz.Recording, rows []graded, copyDurationMS int,
) bool {
	for _, row := range rows {
		if row.recording.ID != clean.ID &&
			explicitEditionOf(clean, row.recording, copyDurationMS) {
			return true
		}
	}
	return false
}

// sharedIdentifier says what stops a proof-grade candidate naming its recording
// on its own: something else fits exactly as well. It is what the reviewer needs
// and cannot work out from the ticks, which is that the row beside this one
// carries the same evidence.
func sharedIdentifier(entry graded, conclusive []graded) string {
	if len(conclusive) < 2 || !entry.level.conclusive() {
		return ""
	}
	if entry.pair.isrc == tagmatch.Agrees {
		for _, other := range conclusive {
			if other.recording.ID != entry.recording.ID && other.pair.isrc == tagmatch.Agrees {
				return "MusicBrainz has the same ISRC on another recording, so it does not name this one alone"
			}
		}
	}
	return "another recording fits this entry exactly as well"
}

// decide concludes what a file is from everything the providers returned.
func decide(evidence Evidence, recordings []musicbrainz.Recording) Outcome {
	found := make([]graded, 0, len(recordings))
	conclusive := make([]graded, 0, 2)
	identified := false
	for _, recording := range recordings {
		// The source said this entry is the explicit recording, so a row
		// MusicBrainz marks as the clean edition is not what was asked for. It is
		// dropped before grading, the way a recording somebody rejected is: it must
		// not be the one conclusive answer and must not be offered as a candidate
		// either (ADR 0032).
		if evidence.Explicit && cleanEdition(recording) {
			continue
		}
		pair := compare(evidence, recording)
		level := gradeOf(pair)
		if level == noMatch {
			continue
		}
		if pair.identifies() && !level.conclusive() {
			identified = true
		}
		entry := graded{recording: recording, pair: pair, level: level}
		found = append(found, entry)
		if level.conclusive() {
			conclusive = append(conclusive, entry)
		}
	}
	sort.SliceStable(found, func(left, right int) bool {
		a, b := found[left], found[right]
		if a.level != b.level {
			return a.level > b.level
		}
		if strengthOf(a.pair) != strengthOf(b.pair) {
			return strengthOf(a.pair) > strengthOf(b.pair)
		}
		if a.pair.durationDeltaMS != b.pair.durationDeltaMS {
			return a.pair.durationDeltaMS < b.pair.durationDeltaMS
		}
		if a.recording.Score != b.recording.Score {
			return a.recording.Score > b.recording.Score
		}
		return a.recording.ID.String() < b.recording.ID.String()
	})

	candidates := make([]Candidate, 0, len(found))
	for index, entry := range found {
		if index >= maxCandidates {
			break
		}
		agreements, disagreements := describeVerdicts(entry.pair)
		candidate := describe(evidence, entry.recording)
		candidate.Rank = index + 1
		candidate.Agrees, candidate.Differs = agreements, disagreements
		candidate.Summary = candidateSentence(
			evidence.subject(), entry.pair, agreements, disagreements,
			sharedIdentifier(entry, conclusive))
		candidates = append(candidates, candidate)
	}

	switch {
	case len(conclusive) == 1:
		return resolvedOutcome(
			evidence, conclusive[0].recording, conclusive[0].pair, conclusive[0].level, "")
	case len(conclusive) > 1:
		// MusicBrainz holds the clean edition beside the explicit one, and the
		// explicit one is what the owner wants (ADR 0032). Asked of an
		// entry alone: an entry is a request for a piece of music, and a file is a
		// piece of music in hand, for which the clean edition is a true answer.
		if evidence.subject() == SubjectEntry {
			if kept := withoutCleanEditions(conclusive, evidence.DurationMS); len(kept) == 1 {
				return resolvedOutcome(evidence, kept[0].recording, kept[0].pair, kept[0].level,
					singledOutByExplicitEdition)
			}
		}
		// Several fit, and the release may still name one of them.
		if named := namedByRelease(conclusive); named != nil {
			return resolvedOutcome(evidence, named.recording, named.pair, named.level, "release")
		}
		return Outcome{
			Status: StatusNeedsReview, Candidates: candidates,
			Summary: fmt.Sprintf(
				"%d recordings fit this %s equally well; the evidence does not choose between them.",
				len(conclusive), evidence.subject()),
		}
	case identified:
		return Outcome{
			Status: StatusConflict, Candidates: candidates,
			Summary: conflictSentence(evidence.subject(), candidates),
		}
	case len(candidates) > 0:
		return Outcome{
			Status: StatusNeedsReview, Candidates: candidates,
			Summary: reviewSentence(evidence.subject(), candidates),
		}
	case !evidence.searchable():
		return Outcome{
			Status: StatusLocalOnly,
			Summary: fmt.Sprintf(
				"The %s carries no identifier and no title to search for, so nothing can be asked about it.%s",
				evidence.subject(), ownedAllTheSame(evidence)),
		}
	}
	return Outcome{
		Status: StatusLocalOnly,
		Summary: fmt.Sprintf("No recording MusicBrainz knows of matches this %s.%s",
			evidence.subject(), ownedAllTheSame(evidence)),
	}
}

// ownedAllTheSame is the reassurance a file has earned and an entry has not. A
// file nobody can name is still music the user owns; an entry nobody can name is
// a want with nothing behind it yet, and telling them they own it would be a lie.
func ownedAllTheSame(evidence Evidence) string {
	if evidence.subject() != SubjectFile {
		return ""
	}
	return " It is owned music all the same."
}

// resolvedOutcome records an identity.
//
// singledOutBy names what chose between several recordings that were all
// proof-grade, and is empty when the evidence named one on its own. It is
// written into the method rather than only into the sentence, because "the one
// recording this ISRC names" and "one of two the ISRC named, picked out by the
// release" are different things to have concluded, and a stored decision has to
// be readable as the reason it was made.
func resolvedOutcome(
	evidence Evidence, recording musicbrainz.Recording, pair comparison, level grade,
	singledOutBy string,
) Outcome {
	agreements, _ := describeVerdicts(pair)
	candidate := describe(evidence, recording)
	method := level.method()
	// The recording first, then how it was identified. Written the other way round
	// the sentence said "identified" twice — "Identified as X by Y, identified by
	// ISRC" — and the two "by"s read as if the ISRC were another performer.
	summary := fmt.Sprintf("%q by %s — %s.",
		candidate.TrackTitle, credited(candidate.ArtistName), level.summary())
	if singledOutBy != "" {
		method += "-and-" + singledOutBy
		summary = fmt.Sprintf("%q by %s — %s, %s",
			candidate.TrackTitle, credited(candidate.ArtistName), level.summary(),
			singledOutSentence(singledOutBy, evidence.subject()))
	}
	return Outcome{
		Status:  StatusResolved,
		Summary: summary,
		Identity: &Identity{
			RecordingID: candidate.RecordingID, ReleaseGroupID: candidate.ReleaseGroupID,
			ArtistName: candidate.ArtistName, ReleaseTitle: candidate.ReleaseTitle,
			TrackTitle: candidate.TrackTitle, DurationMS: candidate.DurationMS,
			ISRC: candidate.ISRC, Method: method, Confidence: level.confidence(),
			Summary: summary, Evidence: agreements,
		},
	}
}

// singledOutSentence finishes the summary of a recording taken out of several
// that fit equally well, saying which rule took it.
func singledOutSentence(singledOutBy, subject string) string {
	if singledOutBy == singledOutByExplicitEdition {
		return "then taken over the clean edition MusicBrainz holds of it."
	}
	return fmt.Sprintf(
		"then singled out from the recordings that fit equally by the %s this %s names.",
		singledOutBy, subject)
}

// describe turns one provider recording into the shape Schall stores, choosing
// the release the file itself names when it names one. A file ripped from a
// compilation should be described by that compilation rather than by the
// edition the provider happens to list first.
func describe(evidence Evidence, recording musicbrainz.Recording) Candidate {
	candidate := Candidate{
		RecordingID: recording.ID,
		ArtistName:  recording.ArtistCredit,
		TrackTitle:  recording.Title,
		DurationMS:  recording.DurationMS,
	}
	if len(recording.ISRCs) > 0 {
		candidate.ISRC = recording.ISRCs[0]
	}
	chosen := -1
	for index, release := range recording.Releases {
		if tagmatch.Title(evidence.Album, release.Title) == tagmatch.Agrees {
			chosen = index
			break
		}
	}
	if chosen < 0 && len(recording.Releases) > 0 {
		chosen = 0
	}
	if chosen >= 0 {
		candidate.ReleaseTitle = recording.Releases[chosen].Title
		candidate.ReleaseGroupID = recording.Releases[chosen].ReleaseGroupID
	}
	return candidate
}

// candidateSentence says what one candidate has going for it and against it.
//
// shared is what stops it when nothing about it disagrees at all. Without it the
// row read "its ISRC, title, artist, release and duration agree, which is not
// enough to identify it on its own" under five ticks and nothing else — true,
// and no help whatever to somebody looking at it, because what is actually in
// the way is the row underneath carrying the same ISRC.
func candidateSentence(
	subject string, pair comparison, agreements, disagreements []string, shared string,
) string {
	switch {
	case len(disagreements) > 0 && len(agreements) > 0:
		return fmt.Sprintf("Its %s agree, but its %s %s.",
			joinWords(agreements), joinWords(disagreements), doVerb(disagreements))
	case len(disagreements) > 0:
		return fmt.Sprintf("Its %s %s.", joinWords(disagreements), doVerb(disagreements))
	case len(agreements) > 0 && shared != "":
		return fmt.Sprintf("Its %s agree, but %s.", joinWords(agreements), shared)
	case len(agreements) > 0 && !pair.contradicted():
		return fmt.Sprintf("Its %s agree, which is not enough to identify it on its own.",
			joinWords(agreements))
	}
	return fmt.Sprintf(
		"Nothing about this recording contradicts the %s, and nothing identifies it either.", subject)
}

func conflictSentence(subject string, candidates []Candidate) string {
	if len(candidates) == 0 {
		return fmt.Sprintf("The %s's own identifiers disagree with what the provider knows.", subject)
	}
	closest := candidates[0]
	if len(closest.Differs) > 0 {
		return fmt.Sprintf("The %s's identifiers name %q by %s, but its %s %s.",
			subject, closest.TrackTitle, credited(closest.ArtistName),
			joinWords(closest.Differs), doVerb(closest.Differs))
	}
	return fmt.Sprintf("The %s's identifiers name %q by %s, but the evidence does not settle it.",
		subject, closest.TrackTitle, credited(closest.ArtistName))
}

func reviewSentence(subject string, candidates []Candidate) string {
	closest := candidates[0]
	if len(candidates) > 1 {
		return fmt.Sprintf("%d recordings could be this %s; the closest is %q by %s.",
			len(candidates), subject, closest.TrackTitle, credited(closest.ArtistName))
	}
	return fmt.Sprintf("The closest recording is %q by %s, but the evidence does not identify it conclusively.",
		closest.TrackTitle, credited(closest.ArtistName))
}

// strengthOf ranks near misses for display only. It never decides whether an
// identity may be accepted; gradeOf does that, and it does not add evidence up.
func strengthOf(pair comparison) int {
	weights := []struct {
		result tagmatch.Verdict
		weight int
	}{
		{pair.acoustic, 120}, {pair.anchor, 120}, {pair.witness, 120},
		{pair.recording, 100}, {pair.isrc, 80},
		{pair.title, 40}, {pair.artist, 30}, {pair.duration, 20}, {pair.release, 10},
	}
	score := 0
	for _, entry := range weights {
		switch entry.result {
		case tagmatch.Agrees:
			score += entry.weight
		case tagmatch.Differs:
			score -= entry.weight
		}
	}
	return score
}

func describeVerdicts(pair comparison) (agreements, disagreements []string) {
	anchor := pair.anchor
	if anchor == tagmatch.Unknown && pair.anchorReproduced {
		anchor = tagmatch.Agrees
	}
	labelled := []struct {
		result tagmatch.Verdict
		label  string
	}{
		{pair.acoustic, acousticLabel(pair)},
		{anchor, anchorLabel(pair)},
		{pair.witness, "audio (the same music as a file you already own)"},
		{pair.recording, "MusicBrainz recording ID"}, {pair.isrc, "ISRC"},
		{pair.title, "title"}, {pair.artist, "artist"},
		{pair.release, "release"}, {pair.duration, durationLabel(pair)},
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

// anchorLabel says what the copy came to beside the audio reference.
// It names the thing compared rather than the number, because the number
// means nothing to anybody reading the row.
func anchorLabel(pair comparison) string {
	if pair.anchorSource == AnchorByName {
		return "audio (matches an upload found by name; a person decides)"
	}
	if pair.anchorSource == AnchorByNameTopic {
		return "audio (matches the artist's Topic upload)"
	}
	if pair.anchor == tagmatch.Differs {
		return "audio (not the published sample of this recording)"
	}
	return "audio (matches the published sample of this recording)"
}

// acousticLabel says what the audio came to, in words that stay true of one
// cluster. A reviewer looking at two candidates has to be told when both are rows
// MusicBrainz keeps for the same audio, because then the question in front of
// them is which row to key the answer on rather than what the file is.
func acousticLabel(pair comparison) string {
	switch {
	case pair.acoustic == tagmatch.Differs:
		return "audio (identified as another recording)"
	case pair.acoustic == tagmatch.Agrees && pair.acousticSimilarity < acousticFloor:
		return fmt.Sprintf("audio (a %d%% likeness, too little to identify it)",
			int(pair.acousticSimilarity*100))
	case pair.acousticNames > 1:
		return fmt.Sprintf("audio (which MusicBrainz enters as %d recordings, this among them)",
			pair.acousticNames)
	}
	return "audio"
}

func durationLabel(pair comparison) string {
	if pair.duration == tagmatch.Differs {
		return fmt.Sprintf("duration (%s out)", roughSeconds(pair.durationDeltaMS))
	}
	return "duration"
}

func roughSeconds(milliseconds int) string {
	seconds := (milliseconds + 500) / 1000
	if seconds == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", seconds)
}

// credited names an artist even when the provider left the credit empty, so a
// sentence about a recording never reads as being about nobody.
func credited(name string) string {
	if strings.TrimSpace(name) == "" {
		return "an unnamed artist"
	}
	return name
}

func doVerb(items []string) string {
	if len(items) == 1 {
		return "disagrees"
	}
	return "disagree"
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

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
