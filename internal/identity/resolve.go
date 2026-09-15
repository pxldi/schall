package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

// searchLimit bounds one provider search. Five candidates is already more than
// a person will read, and the ranking keeps the plausible ones at the top.
const searchLimit = 10

// Provider is the metadata provider resolution asks. It is deliberately narrow:
// resolution looks recordings up, searches for them, and asks which recording an
// ID names now, and does nothing else.
//
// The third question is the merge question, and it is the provider's alone to
// answer. Whether two IDs name one recording is settled where the redirect is
// seen — at the client — and nothing in this package infers it from anything the
// recordings have in common.
type Provider interface {
	Recording(context.Context, uuid.UUID) (musicbrainz.Recording, error)
	SearchRecordings(context.Context, musicbrainz.RecordingQuery, int) ([]musicbrainz.Recording, error)
	ResolveRecordingID(context.Context, uuid.UUID) (uuid.UUID, error)
}

// Resolver asks a provider what one piece of evidence is.
//
// It holds no storage on purpose. A file's tags and a playlist entry are the
// same evidence arriving from different places, and they are resolved by one
// implementation so that no rule can be loosened for one of them without being
// loosened for both. Where the answer is written down is the caller's business:
// identity.Service puts it on a library file, acquisition puts it on a target.
type Resolver struct {
	provider Provider
	aliases  AliasStore
}

func NewResolver(provider Provider) *Resolver {
	return &Resolver{provider: provider}
}

// AliasStore remembers that the provider answered one recording identifier with
// another.
//
// MusicBrainz merges recordings, and a file tagged before a merge carries the
// retired identifier for ever. This path can ask the provider which recording an
// identifier names now; the two places that compare identifiers as literal
// strings — the library join, which is one SQL statement, and import validation,
// which compares two tags — cannot ask anybody. So what the provider says here
// is written down for them to read.
//
// It is the only writer. Nothing may put a row in from a similarity, a
// heuristic or a person, because the row is a copy of the provider's own answer
// and nothing else carries that weight.
type AliasStore interface {
	RecordRecordingAlias(ctx context.Context, retired, surviving uuid.UUID) error
}

// WithAliases registers where merges are remembered. Without it nothing is
// written and every comparison is exactly what it was before: this only ever
// adds evidence.
func (resolver *Resolver) WithAliases(aliases AliasStore) *Resolver {
	resolver.aliases = aliases
	return resolver
}

// rememberMerge writes down that the provider answered one identifier with
// another. It is called wherever an identifier is resolved, because the answer
// is the same answer whatever the caller was comparing it against.
//
// A failure is swallowed. Not remembering a merge costs a later comparison the
// evidence it might have had; failing the resolution over it would cost the file
// its answer outright, and the resolution in hand did not depend on the write.
func (resolver *Resolver) rememberMerge(ctx context.Context, asked, answered uuid.UUID) error {
	if resolver.aliases == nil || asked == uuid.Nil || answered == uuid.Nil || asked == answered {
		return nil
	}
	if err := resolver.aliases.RecordRecordingAlias(ctx, asked, answered); err != nil {
		return fmt.Errorf("record that recording %s is now %s: %w", asked, answered, err)
	}
	return nil
}

// Resolve puts the evidence to the provider, strongest evidence first, and stops
// as soon as something is proven. An identifier that agrees with everything the
// evidence says cannot be bettered by a search, so well-tagged music costs
// exactly one request.
//
// excluded names recordings this evidence is known not to be, because somebody
// said so: a resolution the review queue rejected as the wrong target must not
// be proposed a second time. They are dropped before grading rather than after,
// so an excluded recording cannot be the one conclusive answer and cannot be
// offered as a candidate either.
//
// An error means the provider could not be asked. Everything else is an answer,
// including the answer that no such recording exists.
func (resolver *Resolver) Resolve(
	ctx context.Context, evidence Evidence, excluded ...uuid.UUID,
) (Outcome, error) {
	if !evidence.searchable() {
		return decide(evidence, nil), nil
	}
	skip := make(map[uuid.UUID]bool, len(excluded))
	for _, recordingID := range excluded {
		skip[recordingID] = true
	}

	found := make([]musicbrainz.Recording, 0, searchLimit)
	unknownIdentifier := false

	// The audio is asked first, because it is the strongest thing there is to go
	// on (gradeOf) and the only evidence that is not the file's own account of
	// itself. It is also the only evidence that can name a recording for a file
	// whose tags name nothing at all, which is the whole reason it is read here.
	//
	// The names are taken from the leading cluster and nowhere else. Nothing is
	// graded here — these recordings are looked up and handed to the same decide()
	// every other kind of evidence is handed to, which reads the answer at that one
	// cluster again. A cluster naming several recordings is MusicBrainz holding one
	// performance as several rows: each is looked up, each is conclusive, and which
	// row to key the answer on is then a question for a person rather than a
	// coin toss.
	//
	// A name MusicBrainz has never heard of is passed over rather than made a
	// conflict. An identifier a file carries is a claim the file makes, and a dead
	// one is that claim being wrong; a name AcoustID holds is a submission somebody
	// made to a different database years ago, and MusicBrainz having no such row is
	// silence about this file.
	if best := evidence.Acoustic.best(); best != nil {
		for _, named := range best.Recordings {
			if named == uuid.Nil || skip[named] {
				continue
			}
			recording, err := resolver.provider.Recording(ctx, named)
			if errors.Is(err, musicbrainz.ErrNotFound) {
				continue
			}
			if err != nil {
				return Outcome{}, fmt.Errorf("look up recording %s: %w", named, err)
			}
			found = merge(found, []musicbrainz.Recording{recording}, skip)
		}
		if outcome := decide(evidence, found); outcome.Status == StatusResolved {
			return outcome, nil
		}
	}

	if evidence.RecordingID != uuid.Nil && !skip[evidence.RecordingID] {
		recording, err := resolver.provider.Recording(ctx, evidence.RecordingID)
		switch {
		case errors.Is(err, musicbrainz.ErrNotFound):
			unknownIdentifier = true
		case err != nil:
			return Outcome{}, fmt.Errorf("look up recording %s: %w", evidence.RecordingID, err)
		default:
			if outcome := decide(evidence, []musicbrainz.Recording{recording}); outcome.Status == StatusResolved {
				return outcome, nil
			}
			// merge rather than append: the audio may have named this same
			// recording already, and one recording described twice would be two
			// rows of the same answer in front of the reviewer.
			found = merge(found, []musicbrainz.Recording{recording}, skip)
		}
	}

	if isrc := strings.TrimSpace(evidence.ISRC); isrc != "" {
		recordings, err := resolver.provider.SearchRecordings(
			ctx, musicbrainz.RecordingQuery{ISRC: isrc}, searchLimit)
		if err != nil {
			return Outcome{}, fmt.Errorf("search recordings by ISRC %s: %w", isrc, err)
		}
		found = merge(found, recordings, skip)
		if !unknownIdentifier {
			if outcome := decide(evidence, found); outcome.Status == StatusResolved {
				return outcome, nil
			}
		}
	}

	if title := strings.TrimSpace(evidence.Title); title != "" {
		recordings, err := resolver.provider.SearchRecordings(ctx, musicbrainz.RecordingQuery{
			Artist: evidence.Artist, Title: title,
		}, searchLimit)
		if err != nil {
			return Outcome{}, fmt.Errorf("search recordings for %q: %w", title, err)
		}
		found = merge(found, recordings, skip)
	}

	outcome := decide(evidence, found)
	if unknownIdentifier {
		// Music naming a recording the provider has never heard of is not music
		// without an identity: it is music whose identity is wrong, and that is a
		// different question with a different answer.
		outcome.Status = StatusConflict
		outcome.Summary = fmt.Sprintf(
			"The MusicBrainz recording ID this %s carries is not known to MusicBrainz. %s",
			evidence.subject(), unknownIdentifierAdvice(outcome.Candidates))
	}
	return outcome, nil
}

func unknownIdentifierAdvice(candidates []Candidate) string {
	if len(candidates) == 0 {
		return "Nothing else identifies it either."
	}
	return fmt.Sprintf("The closest recording found instead is %q by %s.",
		candidates[0].TrackTitle, credited(candidates[0].ArtistName))
}

// merge adds recordings that are neither already known nor excluded, keeping the
// order in which the evidence produced them.
func merge(
	known []musicbrainz.Recording, found []musicbrainz.Recording, skip map[uuid.UUID]bool,
) []musicbrainz.Recording {
	seen := make(map[uuid.UUID]bool, len(known))
	for _, recording := range known {
		seen[recording.ID] = true
	}
	for _, recording := range found {
		if !seen[recording.ID] && !skip[recording.ID] {
			seen[recording.ID] = true
			known = append(known, recording)
		}
	}
	return known
}

// ErrRecordingUnknown reports that the provider has never heard of the recording
// a file was to be checked against. It is a statement about the want, not about
// the file: the resolution behind it is wrong, and no file can be proven to be a
// recording nobody can describe.
var ErrRecordingUnknown = errors.New("MusicBrainz does not know the recording this was to be checked against")

// Verification is what one file's evidence came to against one wanted recording.
//
// Identity is the whole answer: it is set when the file is proven to be that
// recording and nil when it is not. Everything else says why, in words, and
// which kind of refusal it was — the two are different questions and they get
// different treatment, because a file the audio contradicts is finished while a
// file nothing could decide is a question.
type Verification struct {
	Identity *Identity
	Summary  string
	Agrees   []string
	Differs  []string
	// Audio reports whether what proved it included the audio. A file a stranger
	// sent may be admitted on nothing less (ADR 0002), and this is how a caller
	// asks without reading the method back out of a string.
	//
	// Two things count as the audio, and they are two witnesses rather than two
	// readings of one: AcoustID naming this recording, and the copy reproducing
	// the excerpt this recording's distributor publishes. Either is the music
	// itself answering.
	Audio bool
	// AudioSaysOtherwise is the one refusal that is about the music rather than
	// about what the file claims: the audio was identified, and it is not this
	// recording.
	AudioSaysOtherwise bool
	// AnchorSaysOtherwise is the other refusal about the music: the audio failed
	// to reproduce the excerpt the distributor publishes for this recording. It is
	// kept apart from AudioSaysOtherwise because they are two witnesses and each
	// answered for itself — reading one as the other would write down a thing
	// AcoustID never said.
	AnchorSaysOtherwise bool
	// AnchorAgrees reports the copy reproducing that excerpt. On its own that
	// proves the copy is the recording, so Identity is set and Audio is true with
	// it; it is reported separately all the same, because what a copy's evidence
	// says has to be readable back whether or not it was the thing that decided.
	//
	// Both flags false means nothing was concluded — no anchor, no comparison, or
	// a distance between the two thresholds, which is a question for a person.
	AnchorAgrees bool
	// WitnessAgrees reports the copy holding the same music as a file the library
	// already holds and has already proven to be this recording. On its own that
	// proves the copy, so Identity is set and Audio is true with it.
	//
	// There is no WitnessSaysOtherwise, and there must not be. A copy far from an
	// owned file is a different master of the same recording as often as it is
	// other music, and this witness is one copy of the music rather than anybody
	// who published it. It admits and it never refuses.
	WitnessAgrees bool
	// Contradicted is the file's own account of itself disagreeing — an
	// identifier naming something else, a credit that is somebody else, or a
	// length this recording cannot have.
	Contradicted bool
	// SameRegistrationUnchecked reports that the audio named another MusicBrainz
	// row and MusicBrainz could not be asked whether that row holds this same
	// registered track. The refusal that would otherwise be final rests on that
	// answer, so without it the copy is a question rather than a verdict
	// (docs/decisions/0031).
	SameRegistrationUnchecked bool
	// AudioSetAside reports that the leading AcoustID cluster named another
	// MusicBrainz row that was established as the same registration. The
	// identification is neither agreement nor contradiction with the wanted row.
	AudioSetAside bool
	// Recording is what the file was measured against, as the provider has it.
	// It is set whatever the verdict, including when there is none, so a question
	// put to somebody can say what the answer was supposed to be rather than only
	// what the file claimed.
	//
	// It is description and never evidence: nothing here grades anything, and a
	// caller reading it is reading the recording, not a conclusion about the file.
	Recording *Candidate
}

// reconcileMergedIdentifier gives the provider the chance to say that the ID the
// file carries and the recording being checked against are one recording after
// all. Resolution never needs this — it looks the file's own ID up and the answer
// arrives already reconciled — but verification looks up the want's recording,
// which is the other ID, and a merge is invisible from that side.
//
// It asks the narrowest question there is — which recording that ID names now —
// so the answer is one the client can remember. The first copy of a want pays for
// it and the copies after it pay nothing, which matters because the provider
// serves one request a second and a want is offered by many peers.
//
// It is asked only on the path that would otherwise end the file for good. A
// recording the provider has never heard of is left disagreeing: that is a file
// naming something that does not exist, which is a real conflict. A provider that
// could not be reached is an error rather than a refusal, because a permanent
// verdict must not be a network outage in disguise.
func (resolver *Resolver) reconcileMergedIdentifier(
	ctx context.Context, evidence Evidence, recording musicbrainz.Recording,
) (musicbrainz.Recording, error) {
	if evidence.RecordingID == uuid.Nil {
		return recording, nil
	}
	tagged, err := resolver.provider.ResolveRecordingID(ctx, evidence.RecordingID)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return recording, nil
	}
	if err != nil {
		return recording, fmt.Errorf("resolve recording %s: %w", evidence.RecordingID, err)
	}
	// Written down whether or not it settles the question in hand. The provider
	// has said which recording this identifier names now, and that is true for
	// every later comparison as much as for this one.
	// A merge lost here costs one more lookup later and nothing else.
	_ = resolver.rememberMerge(ctx, evidence.RecordingID, tagged)
	if tagged != recording.ID {
		return recording, nil
	}
	recording.KnownAs = append(recording.KnownAs, evidence.RecordingID)
	return recording, nil
}

// reconcileMergedIdentification does for the audio what reconcileMergedIdentifier
// does for the tag. AcoustID has been collecting recording IDs for as long as
// MusicBrainz has been merging them, so an identification can perfectly well name
// the ID this recording was merged away from — and read as a difference that is
// the audio naming other music, the one refusal that survives everything else
// agreeing and the one that is final.
//
// It asks the provider what each ID the leading cluster named resolves to, in the
// order the audio gave them, and stops at the first that comes back as this
// recording. So the cost is bounded by how many recordings that cluster held, is
// paid only where the file was about to be struck off for good, and is nothing at
// all in the ordinary case where the audio agreed. A cluster names the same IDs
// for every copy of a want, and the client remembers what they resolved to, so
// the bound is paid once rather than per candidate.
//
// The lower clusters are deliberately not asked about. They are other audio that
// resembled the fingerprint, so reconciling a name out of one would turn "this is
// not the music you wanted" into an admission — the one thing reading the answer
// at its cluster boundary exists to prevent.
//
// Resolution does not do this. Its acoustic contradiction is a question put to
// somebody rather than a verdict, and reconciling it would cost a lookup for every
// name against every candidate.
func (resolver *Resolver) reconcileMergedIdentification(
	ctx context.Context, evidence Evidence, recording musicbrainz.Recording,
) (musicbrainz.Recording, error) {
	best := evidence.Acoustic.best()
	if best == nil {
		return recording, nil
	}
	for _, named := range best.Recordings {
		if named == uuid.Nil {
			continue
		}
		found, err := resolver.provider.ResolveRecordingID(ctx, named)
		if errors.Is(err, musicbrainz.ErrNotFound) {
			continue
		}
		if err != nil {
			return recording, fmt.Errorf("resolve recording %s: %w", named, err)
		}
		_ = resolver.rememberMerge(ctx, named, found)
		if found != recording.ID {
			continue
		}
		recording.KnownAs = append(recording.KnownAs, named)
		return recording, nil
	}
	return recording, nil
}

// registrationCheck is what asking whether the audio named another row of this
// same registered track came to.
//
// rows are the named recordings that hold it. unavailable is a lookup that could
// not be made, which is neither an answer nor a refusal: the copy is held with a
// sentence saying the check did not run.
type registrationCheck struct {
	rows        []musicbrainz.Recording
	unavailable bool
	// explicit are the named rows that hold the explicit edition of the clean
	// row this want names (docs/decisions/0032). They are kept apart from rows
	// because they are a different fact: two registered tracks, not one entered
	// twice, and what settles the copy is that the explicit edition is the one
	// the owner keeps.
	explicit []musicbrainz.Recording
	// gone are the names MusicBrainz no longer has a row for. A fingerprint
	// linked to a deleted row is a stale link, and a row that does not exist
	// cannot be other music, so these are set aside with the siblings.
	gone []uuid.UUID
}

// names are the identifiers set aside: the rows established as this same
// registration, in the order the audio gave them, and the names MusicBrainz no
// longer answers for.
func (check registrationCheck) names() []uuid.UUID {
	named := make([]uuid.UUID, 0, len(check.rows)+len(check.explicit)+len(check.gone))
	for _, row := range append(check.rows, check.explicit...) {
		named = append(named, row.ID)
	}
	return append(named, check.gone...)
}

// answered reports the check having established something about a named row.
func (check registrationCheck) answered() bool {
	return len(check.rows) > 0 || len(check.explicit) > 0
}

// sentence is what a person reads in place of the refusal this used to be. Each
// half says what it established, because the two are different facts.
func (check registrationCheck) sentence() string {
	said := make([]string, 0, 2)
	if len(check.rows) > 0 {
		said = append(said, sameRegistrationSentence(check.rows))
	}
	if len(check.explicit) > 0 {
		said = append(said, explicitEditionSentence(check.explicit))
	}
	return strings.Join(said, " ")
}

// secondRowOfSameRegistration reports the names in the leading cluster that are
// another MusicBrainz row for the same registered track as this recording.
//
// MusicBrainz enters one performance as two recordings and does not always merge
// them. When that happens the fingerprint names one row and the want is keyed to
// the other, and read as a difference that is the audio naming other music — the
// one refusal that survives everything else agreeing, and the one that is final.
// Schall discarded the right music that way 1244 times, on 83 wants.
//
// Two tests establish it, and neither is a resemblance:
//
//   - The named recording carries an ISRC this recording also carries, and the
//     copy reproduces the excerpt this recording's own distributor publishes.
//     Both, because a shared code alone admits something wrong: MusicBrainz puts
//     one ISRC on genuinely different audio too, so the code alone would let a
//     fingerprint of the sped-up edit answer for a want for the original
//     (docs/decisions/0025).
//   - The two rows name the same set of artists, hold the same title once the
//     bracketed labels are dropped, and run the same length. That is
//     docs/decisions/0031, and it needs no excerpt: equal lengths are what the
//     excerpt was there to rule out, and the sped-up edit fails both the title
//     and the length.
//
// A third case is not that question. Where MusicBrainz marks this recording as
// the clean edition of the named row, the two are two registered tracks, and the
// audio is the explicit edition the owner keeps (docs/decisions/0032). The
// identification is set aside there for the same reason and with the same
// effect: it stops being a refusal, and the copy still needs an anchor to be
// admitted.
//
// The excerpt read on the first test has to be the one fetched by this
// recording's own ISRC. An upload found by the want's name can be the sibling
// edit that shares the code, and reading it there would cancel the fingerprint's
// refusal with the very audio the fingerprint was refusing (docs/decisions/0029,
// 0030). The second test admits an upload found by name, because a row-to-row
// length agreement excludes the edit that made the first one strict.
//
// It costs one lookup for each name in the leading cluster, and it is asked only
// where a copy was about to be struck off for good.
//
// Nothing is written down. A merge is the provider's own answer and is
// remembered as such; this is Schall reading two of the provider's rows and
// concluding they hold one registration, which is a conclusion about this
// comparison and not a fact to file under the provider's name.
func (resolver *Resolver) secondRowOfSameRegistration(
	ctx context.Context, evidence Evidence, recording musicbrainz.Recording,
) registrationCheck {
	best := evidence.Acoustic.best()
	if best == nil {
		return registrationCheck{}
	}
	sampled := provenBySample(evidence.Anchor, recording)
	var check registrationCheck
	for _, named := range best.Recordings {
		if named == uuid.Nil || recording.Answers(named) {
			continue
		}
		other, err := resolver.provider.Recording(ctx, named)
		if errors.Is(err, musicbrainz.ErrNotFound) {
			check.gone = append(check.gone, named)
			continue
		}
		if err != nil {
			// One name nobody could look up leaves the whole check unanswered. The
			// row it named may be other music, so the refusal cannot stand on the
			// rows that did come back, and it must not stand on a request that
			// failed either.
			return registrationCheck{unavailable: true}
		}
		switch oneRegistration(recording, other, evidence.DurationMS) {
		case byNameAndLength:
			check.rows = append(check.rows, other)
		case byRegistrationCode:
			if sampled {
				check.rows = append(check.rows, other)
			}
		case byExplicitEdition:
			check.explicit = append(check.explicit, other)
		}
	}
	return check
}

// provenBySample reports the copy reproducing the excerpt this recording's own
// distributor published for it, fetched by its ISRC. It is the reading
// identifiedBySample makes, asked of an anchor before a comparison exists.
//
// Both places that ask whether two rows are one registration read it, so what
// the registration-code test will accept is decided once.
func provenBySample(anchor *AnchorComparison, recording musicbrainz.Recording) bool {
	return anchor != nil && anchor.Source == AnchorSourceDeezer &&
		compareAnchor(anchor, recording) == tagmatch.Agrees
}

// Recording reads one recording from the provider. It is the same lookup every
// comparison here makes, exposed so that whoever writes an identity down can say
// what the recording is without holding a provider of its own.
func (resolver *Resolver) Recording(
	ctx context.Context, id uuid.UUID,
) (musicbrainz.Recording, error) {
	return resolver.provider.Recording(ctx, id)
}

// FilingVerdict is what asking about a want's recording and the recording the
// library filed that want's copy under came to.
type FilingVerdict int

const (
	// FilingOtherRecording is two recordings the audio cannot separate. The want
	// stops and a person decides which of them this file is.
	FilingOtherRecording FilingVerdict = iota
	// FilingOneRegistration is one registered track MusicBrainz entered twice
	// (docs/decisions/0025, 0031). The file holds the music the want asked for,
	// so it is filed under the want's recording and the want settles.
	FilingOneRegistration
	// FilingExplicitEdition is the want naming the clean edition of the track the
	// file holds (docs/decisions/0032). The want settles against the file as the
	// library has it, and the file keeps the recording the library gave it:
	// re-filing it under the clean row would write down that this audio is a
	// recording it is not.
	FilingExplicitEdition
	// FilingMerged is MusicBrainz having merged the two rows. A lookup on either
	// identifier arrives at one row, which is the provider's own statement that
	// they name one recording, and it is the only thing here that is not Schall
	// reading two rows and drawing a conclusion. The merge is written down and
	// the want settles.
	FilingMerged
)

// FilingEvidence is what the want's own record says about the copy on its disc.
//
// It is handed in rather than looked up, because every field is a fact the
// acquisition side already holds about the copy it accepted, and asking the
// provider for it a second time would not make it truer.
type FilingEvidence struct {
	// Anchor is the excerpt the copy was measured against when it was admitted,
	// with who published it. It is left out unless it was fetched for the
	// recording the want names now.
	Anchor *AnchorComparison
	// CopyDurationMS is how long the file on the disc runs. It stands in for a
	// length MusicBrainz does not hold for one of the two rows, and it is read
	// nowhere else.
	CopyDurationMS int
	// Method is what admitted the copy against the recording the want names: the
	// grader's own name for it, or "manual" where a person listened. It is empty
	// where nothing admitted the copy, and where the copy was judged against a
	// recording the want no longer names.
	Method string
}

// proven reports the copy having been admitted on grounds outside the
// catalogue's bookkeeping: its own sound, or a person's ear.
func (evidence FilingEvidence) proven() bool { return ProvenForItsWant(evidence.Method) }

// FilingProof is why a library file is being filed under a want's recording. It
// is written onto the identity as its evidence, so the row reads back as the
// reason it was made.
//
// It is handed in because only the caller knows. FileAsAcquired writes an answer
// somebody else established, and a list it made up for itself is how the old
// version came to say "ISRC" about pairs that share no code at all.
type FilingProof struct {
	// Merged is MusicBrainz having merged the recording the library filed the
	// file under into this one. It is the provider's own answer, and the only
	// thing here that is not Schall reading rows.
	Merged bool
	// SecondRow is the library having filed the file under a second MusicBrainz
	// row that Schall read as the same registered track. False where the library
	// wrote nothing down at all, because then there is no second row to speak of.
	SecondRow bool
	// Method is what admitted the copy against this recording, in the grader's own
	// name for it. Empty where nothing about the copy is recorded, and an empty
	// method writes no evidence rather than a sentence standing in for one.
	Method string
}

// OneRegistration reports whether the recording a want names and the recording
// the library filed that want's copy under are one registered track entered
// twice.
//
// It is asked at one moment: a want about to stop for ever with its own music on
// the disc. A want names one recording, something proved a copy was it, the copy
// was imported, and the library then filed the file it became under a second
// MusicBrainz row. Read as a difference that leaves the want stopped: 32 wants
// sat there on 2026-09-05.
//
// Two things file the copy, and neither is a resemblance between the two rows:
//
//   - MusicBrainz merged them. A lookup on either identifier arrives at one row,
//     because the provider answers a merged-away identifier with the recording
//     it was merged into. That is the catalogue's own answer, it is written down
//     as a merge, and nothing else here reaches it.
//   - Something proved the copy is the recording the want names, and the second
//     row was all that stood in the way. The audio answered — AcoustID at
//     the leading cluster, the excerpt this recording's distributor publishes, a
//     file the library already holds and has proven — or a person listened and
//     said so. Then the two rows have to be one registration as well
//     (oneRegistration), because a copy proved against the want's row and filed
//     under a row that contradicts it is a question either way.
//
// The registration test is a check on that proof and never a substitute for it.
// Two rows of one name, one credit and one length are what MusicBrainz's
// duplicates look like, and they are also what a re-recording and a live take of
// the same length look like. Whether the file on the disc is this want's music is
// answered by what admitted the file, so a copy nothing proved stays a question
// for a person however alike the two rows read.
//
// The ISRC branch keeps its excerpt on top of that. A shared code alone admits
// other music — MusicBrainz puts the ISRC of "1992" on "1992 (sped Up + slowed
// mixes)" as well — and only the excerpt this recording's own distributor
// published for that code separates them (docs/decisions/0025).
//
// A fourth answer is not this question at all. Where the two rows differ only in
// that MusicBrainz marks the want's row as the clean edition of the filed one,
// the file holds the explicit edition and the owner keeps that one, so the want
// settles against it (docs/decisions/0032). The file keeps the recording the
// library gave it, and nothing is filed anywhere. The other direction never
// settles: a want for the explicit edition goes on looking when its copy was
// filed as the clean one.
//
// Both rows are looked up every time. That is two requests against a provider
// serving one a second, paid for a want that is otherwise about to stop for a
// person, and it is what the merge is read from.
//
// Nothing is written down about a pair the metadata bridges. That is Schall
// reading two of the provider's rows and concluding they hold one registration,
// which is a conclusion about this comparison and not a fact to file under
// MusicBrainz's name. A merge is the opposite and is remembered as such.
func (resolver *Resolver) OneRegistration(
	ctx context.Context, wanted, filed uuid.UUID, evidence FilingEvidence,
) (FilingVerdict, error) {
	if wanted == uuid.Nil || filed == uuid.Nil || wanted == filed {
		return FilingOtherRecording, nil
	}
	wantedRecording, err := resolver.provider.Recording(ctx, wanted)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return FilingOtherRecording, nil
	}
	if err != nil {
		return FilingOtherRecording, fmt.Errorf("look up recording %s: %w", wanted, err)
	}
	filedRecording, err := resolver.provider.Recording(ctx, filed)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return FilingOtherRecording, nil
	}
	if err != nil {
		return FilingOtherRecording, fmt.Errorf("look up recording %s: %w", filed, err)
	}
	// Two identifiers that arrive at one row are the provider redirecting one to
	// the other, which is the whole of the evidence for a merge. It is read before
	// anything the rows say, because it is the only answer here MusicBrainz itself
	// gave, and it is written down for every later comparison rather than only
	// this one. A merge that cannot be written down files nothing: the caller
	// comes back on a later sweep, and the record is what wakes the other wants
	// held on the retired row.
	if wantedRecording.ID == filedRecording.ID {
		if err := resolver.rememberMerge(ctx, wanted, wantedRecording.ID); err != nil {
			return FilingOtherRecording, err
		}
		if err := resolver.rememberMerge(ctx, filed, filedRecording.ID); err != nil {
			return FilingOtherRecording, err
		}
		return FilingMerged, nil
	}
	switch oneRegistration(wantedRecording, filedRecording, evidence.CopyDurationMS) {
	case byExplicitEdition:
		return FilingExplicitEdition, nil
	case byNameAndLength:
		if evidence.proven() {
			return FilingOneRegistration, nil
		}
	case byRegistrationCode:
		if provenBySample(evidence.Anchor, wantedRecording) {
			return FilingOneRegistration, nil
		}
	}
	return FilingOtherRecording, nil
}

// Proven reports whether the file is the recording.
func (verification Verification) Proven() bool { return verification.Identity != nil }

// Verify asks whether one file is the recording somebody is waiting for.
//
// It is the second half of ADR 0001. Resolution proposes a recording from the
// weakest evidence in the system — the words a streaming service wrote — and
// this checks a file against that proposal independently, with the same rules
// and the same grader, so neither side can certify the other's work.
//
// The candidate space is deliberately one recording. The question is not what
// the file is but whether it is this, and a file that turns out to be something
// else is refused here rather than identified: what it actually is stays
// Resolve's question, asked about the file in the library it may yet become.
func (resolver *Resolver) Verify(
	ctx context.Context, evidence Evidence, recordingID uuid.UUID,
) (Verification, error) {
	if recordingID == uuid.Nil {
		return Verification{}, ErrRecordingUnknown
	}
	recording, err := resolver.provider.Recording(ctx, recordingID)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return Verification{}, ErrRecordingUnknown
	}
	if err != nil {
		return Verification{}, fmt.Errorf("look up recording %s: %w", recordingID, err)
	}

	pair := compare(evidence, recording)
	if pair.recording == tagmatch.Differs {
		recording, err = resolver.reconcileMergedIdentifier(ctx, evidence, recording)
		if err != nil {
			return Verification{}, err
		}
		pair = compare(evidence, recording)
	}
	if pair.acoustic == tagmatch.Differs {
		recording, err = resolver.reconcileMergedIdentification(ctx, evidence, recording)
		if err != nil {
			return Verification{}, err
		}
		pair = compare(evidence, recording)
	}
	// Still naming other music. Two witnesses about audio cannot both be right,
	// and the ordinary reason they disagree is that MusicBrainz holds one
	// performance as two rows that were never merged. Establishing that is a
	// lookup, and it is asked here alone: at the point a copy is about to be
	// refused for good.
	//
	// An excerpt that already refused the copy skips it. The copy is finished on
	// the publisher's own audio whatever the catalogue's bookkeeping says, and the
	// lookup would buy nothing.
	registration := ""
	unchecked := false
	setAside := false
	if (pair.acoustic == tagmatch.Differs ||
		(pair.acoustic == tagmatch.Unknown && pair.acousticNames > 1)) &&
		pair.anchor != tagmatch.Differs {
		check := resolver.secondRowOfSameRegistration(ctx, evidence, recording)
		switch {
		case check.unavailable:
			unchecked, registration = true, registrationUncheckedSentence
		case check.answered():
			pair = compareAgainst(evidence, recording, check.names())
			setAside = true
			registration = check.sentence()
		case len(check.gone) > 0:
			pair = compareAgainst(evidence, recording, check.names())
		}
	}
	level := gradeOf(pair)
	agrees, differs := describeVerdicts(pair)
	described := describe(evidence, recording)
	verification := Verification{
		Agrees: agrees, Differs: differs,
		Audio: level == byFingerprint || level == byFingerprintAndIdentifier ||
			level == bySample || level == byLibraryWitness,
		AudioSaysOtherwise:  pair.acoustic == tagmatch.Differs,
		AnchorSaysOtherwise: pair.anchor == tagmatch.Differs,
		AnchorAgrees:        pair.anchorReproduced,
		WitnessAgrees:       pair.witness == tagmatch.Agrees,
		Contradicted:        pair.contradicted(),

		SameRegistrationUnchecked: unchecked,
		AudioSetAside:             setAside,

		Recording: &described,
	}
	if level.conclusive() {
		outcome := resolvedOutcome(evidence, recording, pair, level, "")
		verification.Identity, verification.Summary = outcome.Identity, outcome.Summary
	} else {
		verification.Summary = candidateSentence(evidence.subject(), pair, agrees, differs, "")
	}
	// What the audio was identified as, said in words, because the ticks no
	// longer carry it: a row set aside leaves nothing in the verdicts at all, and
	// somebody reading why this copy was let in — or why it is still a question —
	// has to be told AcoustID named the catalogue's other row.
	if registration != "" {
		verification.Summary = strings.TrimSpace(verification.Summary + " " + registration)
		if verification.Identity != nil {
			verification.Identity.Summary = verification.Summary
		}
	}
	return verification, nil
}
