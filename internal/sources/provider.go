// Package sources models search-only download providers. A provider inspects
// remote catalogues and reports candidates; it never starts a transfer.
package sources

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotConfigured reports that no provider has been configured yet.
var ErrNotConfigured = errors.New("no download source provider is configured")

// ErrRateLimited reports that the provider refused because it is being asked
// too often. It is backpressure, not a failure: the release was not searched,
// nothing was learned about it, and the same request will work once the
// provider has had a moment. Callers must wait and ask again rather than
// recording an answer.
var ErrRateLimited = errors.New("the download source is rate limiting requests")

// ErrProviderUnavailable reports that the provider answered, and what it
// answered is that it is in no state to search: reachable, authenticated, and
// unable to ask anybody anything.
//
// It is backpressure for the same reason rate limiting is. Nothing was asked,
// so nothing was learned about the music, and a caller that records an answer
// here records one the provider never gave. What separates the two is only what
// has to pass for the next request to work, and both pass on their own.
var ErrProviderUnavailable = errors.New("the download source cannot search at the moment")

// ErrPhraseHeldBack reports that the provider would not say these particular
// words again yet.
//
// It is the narrowest refusal there is, and the only one that is not about the
// source. The source is searching, and answering; what it declined is one
// phrase it was asked a moment ago, because repeating a search too soon is what
// gets a Soulseek account banned. So it is a fact about the one thing that
// asked, and a caller holding a queue of other things to ask should ask them.
//
// It wraps ErrProviderUnavailable, so every caller that only wants to know
// "was this backpressure rather than an answer" keeps the answer it had.
var ErrPhraseHeldBack = fmt.Errorf(
	"%w: the same words were searched for too recently to ask again", ErrProviderUnavailable)

// ErrNobodyWasAsked reports that the search was created, spent its whole reply
// window, and never left the machine it was created on.
//
// slskd is built on Soulseek.NET, which moves a search through named states.
// "Queued" is set while the search waits for a slot inside the client, before
// the request has been written to the Soulseek server at all; "InProgress" is
// set only once that write has happened. A search still queued when its window
// closed was therefore put to nobody, and the empty list it hands back says
// nothing whatever about who is sharing the music.
//
// It is backpressure about the one search, not the account: slskd's own queue
// held this search back before it ever reached Soulseek, and every other search
// running at the same time can perfectly well have gone out and come back with
// an answer. A caller with other wants to look for should look for them; this
// one comes back on its own.
var ErrNobodyWasAsked = fmt.Errorf(
	"%w: the search was never put to the Soulseek network", ErrProviderUnavailable)

// ErrNotLoggedIn reports that slskd was reachable but signed out of Soulseek
// while a search ran, so the search closed with nothing because there was no
// connection to send it over.
//
// Unlike ErrNobodyWasAsked, this is about the account, not the one search:
// every search running while slskd is signed out fails the same way, and it
// stays that way until something signs the account back in.
//
// It wraps ErrProviderUnavailable, so every caller that only wants to know
// "was this backpressure rather than an answer" keeps the answer it had.
var ErrNotLoggedIn = fmt.Errorf(
	"%w: slskd was not logged in to the Soulseek network", ErrProviderUnavailable)

// ErrSignedOut reports that slskd itself refused to open a search because its
// connection to Soulseek was not logged in — learned before any search was
// created, from what slskd last said about its own connection, rather than
// after one came back empty (see ErrNotLoggedIn).
//
// It wraps ErrProviderUnavailable rather than ErrNotLoggedIn on purpose.
// ErrNotLoggedIn already earns the account-wide bannedRest wait a caller gives
// a fact about the whole account; this is the same fact learned earlier, by a
// cheaper check that runs before every search, and giving it a different wait
// would only mean the wait depended on which of the two checks got there
// first.
var ErrSignedOut = fmt.Errorf(
	"%w: slskd is not logged in to the Soulseek network", ErrProviderUnavailable)

// ErrSearchSilenced reports that the provider stopped searching because
// searches were reaching the network and coming back empty, one after another,
// for longer than an ordinary quiet stretch ever lasts.
//
// It is the one refusal nobody said out loud. The Soulseek server serves some
// of its half-hour bans by dropping this account's queries while leaving the
// connection up: slskd goes on accepting searches, /server goes on reporting
// "Connected, LoggedIn", and every search collects nothing. Read one at a time
// those searches are ordinary empty answers, and recording them as such writes
// absences about music plenty of peers are sharing. Read as a run they are the
// ban, and the provider stops asking until it has passed.
//
// It is backpressure for the same reason the rest are: no search was made, so
// nothing was learned about the music, and no attempt may be spent on it. It
// wraps ErrProviderUnavailable, so every caller that only wants to know
// "was this backpressure rather than an answer" keeps the answer it had.
var ErrSearchSilenced = fmt.Errorf(
	"%w: the download source's searches are reaching the network and coming back empty",
	ErrProviderUnavailable)

// ErrRepliesUnread reports that peers answered the search and the provider had
// not stored their replies by the time it was asked for them.
//
// slskd keeps a search's responses only once the search reaches a completed
// state: while it is still running, the responses endpoint answers an empty
// list however many replies the search itself has counted. A search Schall
// stopped watching early is asked to finish and waited out, and this is what is
// left when that wait runs out with the search still running.
//
// It is backpressure for the reason every refusal here is: peers did answer, so
// the empty list is not an absence, and recording "nobody is sharing this" from
// it would be a false negative about music that is plainly on offer. It is
// about the one search — every other want in a pass asks in words of its own —
// and the same phrase asked again finds the same peers.
//
// It wraps ErrProviderUnavailable, so every caller that only wants to know
// "was this backpressure rather than an answer" keeps the answer it had.
var ErrRepliesUnread = fmt.Errorf(
	"%w: the search collected replies the download source had not stored", ErrProviderUnavailable)

// ErrSearchBudgetExhausted reports that provider admission left too little of
// the caller's deadline to finish a search. Nothing was asked, so bounded
// ladder callers treat it as exhaustion rather than provider failure.
var ErrSearchBudgetExhausted = errors.New("not enough search budget remains")

// Settings are the stored connection details a provider is built from.
type Settings struct {
	BaseURL       string
	APIKey        string
	SearchTimeout time.Duration
}

// Builder turns stored settings into a usable provider, so the provider in use
// can be swapped without changing anything that consumes one.
type Builder interface {
	Build(Settings) (Provider, error)
}

// Query describes the music Schall is looking for. ExpectedTrackCount is the
// catalogue track count when it is known, and zero otherwise.
//
// Tracks are the catalogue's own tracks, when the caller has them. They are
// what a candidate is corroborated against: without them a search can only
// report how good a copy is, never whether it is the right music.
type Query struct {
	Text               string
	ExpectedTrackCount int
	Tracks             []QueryTrack
}

// QueryTrack is one catalogue track, reduced to the two things a remote peer
// discloses about a file it has not sent yet: what it is called and how long
// it runs.
type QueryTrack struct {
	Title           string
	DurationSeconds int
}

// File is a single audio file offered by a remote peer. Path is the remote
// name exactly as the peer reported it, separators and all, because that is
// what a transfer has to ask for; Name is the readable part shown to the user.
type File struct {
	Path            string
	Name            string
	Extension       string
	SizeBytes       int64
	BitRate         int
	DurationSeconds int
	VariableBitRate bool
}

// Candidate is one inspectable group of files offered by one peer, normally a
// single release folder.
type Candidate struct {
	Provider       string
	Username       string
	Directory      string
	Files          []File
	TotalSizeBytes int64
	Format         string
	AverageBitRate int
	FreeUploadSlot bool
	QueueLength    int
	UploadSpeed    int64
	Score          float64
	Reasons        []string
	// Match is the evidence that this folder holds the release searched for.
	// It is deliberately separate from Score: one says whether this is the
	// right music, the other says how good a copy it is, and conflating them
	// is what makes a well-made copy of the wrong release look like a match.
	Match Match
}

// TrackCount is the number of audio files the candidate offers.
func (candidate Candidate) TrackCount() int {
	return len(candidate.Files)
}

// Status is the outcome of a bounded provider connection check.
type Status struct {
	Detail string
}

// Provider is a search-only source of download candidates.
type Provider interface {
	Name() string
	CheckConnection(context.Context) (Status, error)
	// SearchBudget is the allowance Search needs after shared provider admission.
	// Ladder callers check it before asking, and providers check it again after
	// admission; a provider whose admission leaves less returns
	// ErrSearchBudgetExhausted without starting the search.
	SearchBudget() time.Duration
	Search(context.Context, Query) ([]Candidate, error)
}

// Transfer states Schall understands. A provider reports its own vocabulary;
// these are what the rest of Schall reasons about.
const (
	TransferQueued      = "queued"
	TransferDownloading = "downloading"
	TransferCompleted   = "completed"
	TransferFailed      = "failed"
	TransferCancelled   = "cancelled"
)

// Transfer is the state of one file transfer at a provider. Detail carries the
// provider's own words, so a failure can be explained without guessing.
type Transfer struct {
	ID               string
	Path             string
	State            string
	Detail           string
	SizeBytes        int64
	TransferredBytes int64
	// Deferred reports a transfer the peer would not take *now*, as opposed to
	// one it would not take at all. It is the transfer-layer twin of a 429: the
	// peer has the file and would send it happily, and what it is objecting to is
	// how much we asked of it at once. The state is still a failure, because the
	// bytes did not arrive, but nothing was learned about the music or about
	// whether that peer is worth asking again.
	Deferred bool
}

// Settled reports whether the transfer has stopped moving, either way.
func (transfer Transfer) Settled() bool {
	switch transfer.State {
	case TransferCompleted, TransferFailed, TransferCancelled:
		return true
	}
	return false
}

// Downloader is a provider that can also transfer the files it found. It is
// separate from Provider because a search-only provider stays useful, and
// because nothing outside this interface may start a transfer.
type Downloader interface {
	Provider
	StartDownloads(ctx context.Context, username string, files []File) error
	Transfers(ctx context.Context, username string) ([]Transfer, error)
	CancelDownload(ctx context.Context, username, transferID string) error
}
