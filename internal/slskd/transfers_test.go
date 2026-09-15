package slskd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/sources"
)

func TestNormalizeTransferState(t *testing.T) {
	// The left column is slskd's own vocabulary: a comma-joined set of states.
	for state, want := range map[string]string{
		"Completed, Succeeded":   sources.TransferCompleted,
		"Completed, Errored":     sources.TransferFailed,
		"Completed, TimedOut":    sources.TransferFailed,
		"Completed, Rejected":    sources.TransferFailed,
		"Completed, Cancelled":   sources.TransferCancelled,
		"InProgress":             sources.TransferDownloading,
		"Requested":              sources.TransferQueued,
		"Queued, Remotely":       sources.TransferQueued,
		"Queued, Locally":        sources.TransferQueued,
		"Initializing":           sources.TransferQueued,
		"":                       sources.TransferQueued,
		"Something New Entirely": sources.TransferQueued,
		// Finished without a word saying it succeeded proves nothing arrived.
		"Completed": sources.TransferFailed,
	} {
		if got := normalizeTransferState(state); got != want {
			t.Errorf("normalizeTransferState(%q) = %q, want %q", state, got, want)
		}
	}
}

// An unknown state must never read as completed: treating it as arrived is the
// one mistake that would let Schall believe it owns music it does not have.
func TestNormalizeTransferStateNeverInventsSuccess(t *testing.T) {
	for _, state := range []string{"", "Weird", "Completed, Unheard Of", "Paused"} {
		if normalizeTransferState(state) == sources.TransferCompleted {
			t.Errorf("state %q was read as completed", state)
		}
	}
}

// "Too many files" is the peer counting how much of it we already have queued.
// It is backpressure, and reading it as a refusal is how one folder asked for
// ten times over turns into ten rows saying nobody is sharing the music.
func TestARejectionForTooManyFilesIsDeferredRatherThanRefused(t *testing.T) {
	if !peerDeferred("Completed, Rejected", "Transfer rejected: Too many files") {
		t.Error("a peer refusing for load was read as refusing outright")
	}
}

// The byte-counted half of the same limit. A peer that measures its queue in
// megabytes is saying exactly what one measuring it in files is.
func TestARejectionForTooManyMegabytesIsDeferred(t *testing.T) {
	if !peerDeferred("Completed, Rejected", "Transfer rejected: Too many megabytes") {
		t.Error("a peer refusing for load was read as refusing outright")
	}
}

// "Overwhelmed with requests" is slskd's own wording for the same load limit
// as "too many files" and "too many megabytes". Reading it as a plain failure
// rests the want a full day instead of the 15 minutes backpressure gets.
func TestARejectionForBeingOverwhelmedIsDeferred(t *testing.T) {
	if !peerDeferred("Completed, Rejected", "Overwhelmed with requests; try again later") {
		t.Error("a peer refusing for load was read as refusing outright")
	}
}

// A file the peer no longer shares is a refusal about the file. Asking again in
// a quarter of an hour would only be told the same thing.
func TestARejectionForAFileNoLongerSharedIsNotDeferred(t *testing.T) {
	if peerDeferred("Completed, Rejected", "Transfer rejected: File not shared.") {
		t.Error("a file the peer no longer shares was read as backpressure")
	}
}

// Being banned is a refusal about us, and the one rejection that outlasts any
// delay Schall could wait.
func TestARejectionForBeingBannedIsNotDeferred(t *testing.T) {
	if peerDeferred("Completed, Rejected", "Transfer rejected: Banned") {
		t.Error("a ban was read as backpressure")
	}
}

// Anything nobody here recognises stays a failure. Mistaking a real refusal for
// backpressure is the expensive direction: it asks the same peer forever.
func TestAnUnfamiliarRejectionIsNotDeferred(t *testing.T) {
	for _, reason := range []string{"", "Transfer rejected: Remote file error", "Disallowed extension"} {
		if peerDeferred("Completed, Rejected", reason) {
			t.Errorf("rejection %q was read as backpressure", reason)
		}
	}
}

// The state has to say the peer rejected it. A transfer that timed out while a
// queue limit happened to be mentioned elsewhere is not the peer declining.
func TestAFailureThatIsNotARejectionIsNotDeferred(t *testing.T) {
	if peerDeferred("Completed, TimedOut", "too many files") {
		t.Error("a timeout was read as backpressure")
	}
}

// The deferral has to survive the read, because everything downstream of it
// works from the transfer rather than from slskd's own words.
func TestTransfersReportsAPeerRefusingForLoadAsDeferred(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `[{"id":"t1","filename":"a.flac",
			"state":"Completed, Rejected","size":9,"bytesTransferred":0,
			"exception":"Transfer rejected: Too many files"}]`)
	}))
	defer server.Close()

	transfers, err := testClient(t, server.URL).Transfers(context.Background(), "peer")
	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 1 || !transfers[0].Deferred {
		t.Fatalf("transfers = %#v", transfers)
	}
	// Still a failure: no bytes arrived, and nothing may read this as a copy.
	if transfers[0].State != sources.TransferFailed {
		t.Fatalf("state = %q", transfers[0].State)
	}
}

func TestTransfersReadsTheDirectoryGroupedShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != apiPrefix+"/transfers/downloads/peer" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = io.WriteString(response, `{
			"username": "peer",
			"directories": [{
				"directory": "@@peer\\Music\\Dummy",
				"fileCount": 2,
				"files": [
					{"id": "t1", "filename": "@@peer\\Music\\Dummy\\01.flac",
					 "state": "InProgress", "size": 100, "bytesTransferred": 40},
					{"id": "t2", "filename": "@@peer\\Music\\Dummy\\02.flac",
					 "state": "Completed, Errored", "size": 200, "bytesTransferred": 0,
					 "exception": "peer went offline"}
				]
			}]
		}`)
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	transfers, err := client.Transfers(context.Background(), "peer")
	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 2 {
		t.Fatalf("transfers = %#v", transfers)
	}
	if transfers[0].ID != "t1" || transfers[0].State != sources.TransferDownloading ||
		transfers[0].TransferredBytes != 40 || transfers[0].SizeBytes != 100 {
		t.Fatalf("first transfer = %#v", transfers[0])
	}
	// The remote path is preserved exactly, separators included, because it is
	// the key everything else matches on.
	if transfers[0].Path != `@@peer\Music\Dummy\01.flac` {
		t.Fatalf("path = %q", transfers[0].Path)
	}
	if transfers[1].State != sources.TransferFailed ||
		!strings.Contains(transfers[1].Detail, "peer went offline") {
		t.Fatalf("second transfer = %#v", transfers[1])
	}
}

// The version field taught this project that assuming one shape and testing the
// same assumption proves nothing, so a flat list is accepted too.
func TestTransfersReadsAFlatList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response,
			`[{"id":"t1","filename":"a.flac","state":"Completed, Succeeded","size":9,"bytesTransferred":9}]`)
	}))
	defer server.Close()

	transfers, err := testClient(t, server.URL).Transfers(context.Background(), "peer")
	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 1 || transfers[0].State != sources.TransferCompleted {
		t.Fatalf("transfers = %#v", transfers)
	}
}

// slskd forgets a peer once it has nothing for it, and a forgotten peer is not
// a failure.
func TestTransfersTreatsAForgottenPeerAsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	transfers, err := testClient(t, server.URL).Transfers(context.Background(), "peer")
	if err != nil {
		t.Fatalf("error = %v, want a forgotten peer to read as empty", err)
	}
	if len(transfers) != 0 {
		t.Fatalf("transfers = %#v", transfers)
	}
}

func TestStartDownloadsSendsTheRemotePathVerbatim(t *testing.T) {
	var body []byte
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		// EscapedPath, because a username may contain characters that have to
		// survive the URL intact.
		method, path = request.Method, request.URL.EscapedPath()
		body, _ = io.ReadAll(request.Body)
		response.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	err := testClient(t, server.URL).StartDownloads(context.Background(), "a peer", []sources.File{
		{Path: `@@peer\Music\Dummy\01.flac`, SizeBytes: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != apiPrefix+"/transfers/downloads/a%20peer" {
		t.Fatalf("%s %s", method, path)
	}
	var sent []struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].Filename != `@@peer\Music\Dummy\01.flac` || sent[0].Size != 100 {
		t.Fatalf("sent = %#v (body %s)", sent, body)
	}
}

func TestStartDownloadsRefusesAFileWithoutARemotePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("slskd was asked to enqueue a file with no remote path")
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	if err := client.StartDownloads(context.Background(), "peer", []sources.File{{Name: "01.flac"}}); err == nil {
		t.Fatal("expected an error for a file with no remote path")
	}
	if err := client.StartDownloads(context.Background(), "peer", nil); err == nil {
		t.Fatal("expected an error for an empty file list")
	}
	if err := client.StartDownloads(context.Background(), "", []sources.File{{Path: "a"}}); err == nil {
		t.Fatal("expected an error for a missing username")
	}
}

func TestCancelDownloadAcceptsAnAlreadyForgottenTransfer(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		response.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	if err := testClient(t, server.URL).CancelDownload(context.Background(), "peer", "t1"); err != nil {
		t.Fatalf("error = %v, want a forgotten transfer to count as stopped", err)
	}
	if path != apiPrefix+"/transfers/downloads/peer/t1" {
		t.Fatalf("path = %s", path)
	}
}

// A folder larger than this is not a release, and slskd is not handed an
// unbounded list of files to enqueue in one request.
func TestStartDownloadsRefusesMoreFilesThanOneRequestMayEnqueue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("slskd was handed an unbounded list of files")
	}))
	defer server.Close()

	files := make([]sources.File, 0, maxTransferFiles+1)
	for index := range maxTransferFiles + 1 {
		files = append(files, sources.File{Path: fmt.Sprintf(`@@peer\%d.flac`, index)})
	}

	if err := testClient(t, server.URL).StartDownloads(context.Background(), "peer", files); err == nil {
		t.Fatal("expected an error for more files than one request may enqueue")
	}
}

// A transfer slskd would not start is a transfer that is not running, and the
// caller has to hear that rather than watch for bytes that will never arrive.
func TestStartDownloadsReportsATransferSlskdWouldNotStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := testClient(t, server.URL).StartDownloads(context.Background(), "peer",
		[]sources.File{{Path: `@@peer\01.flac`, SizeBytes: 100}})

	if err == nil || !strings.Contains(err.Error(), "slskd returned HTTP 500") {
		t.Fatalf("error = %v, want the refusal reported", err)
	}
}

func TestTransfersRequireAPeerToAskAbout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("slskd was asked about no peer at all")
	}))
	defer server.Close()

	if _, err := testClient(t, server.URL).Transfers(context.Background(), "  "); err == nil {
		t.Fatal("slskd was asked anyway")
	}
}

// slskd having a bad afternoon is not a peer with no transfers. Reading it as
// one would have Schall conclude a download it started had disappeared.
func TestTransfersReportAFailureRatherThanAnEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	transfers, err := testClient(t, server.URL).Transfers(context.Background(), "peer")

	if err == nil {
		t.Fatal("a failure was reported as a peer with nothing in flight")
	}
	if transfers != nil {
		t.Errorf("transfers = %#v, want nothing alongside the failure", transfers)
	}
}

// A body in neither shape slskd has ever used is a failure, because guessing at
// a third would be inventing transfers.
func TestATransfersBodyInNoShapeAtAllIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `"neither a list nor an object"`)
	}))
	defer server.Close()

	if _, err := testClient(t, server.URL).Transfers(context.Background(), "peer"); err == nil {
		t.Fatal("a body in no shape at all was read as transfers")
	}
}

// The reason a transfer failed is the peer's own words, and a peer can say a
// great deal. It is cut so a single failure cannot fill a response.
func TestTheReasonATransferFailedIsKeptToAReadableLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `[{"id":"t1","filename":"a.flac","state":"Completed, Errored",`+
			`"exception":"`+strings.Repeat("z", 4096)+`"}]`)
	}))
	defer server.Close()

	transfers, err := testClient(t, server.URL).Transfers(context.Background(), "peer")
	if err != nil {
		t.Fatal(err)
	}
	if len(transfers[0].Detail) > 500 {
		t.Fatalf("detail is %d characters long, want it kept readable", len(transfers[0].Detail))
	}
	if !strings.HasPrefix(transfers[0].Detail, "Completed, Errored: ") {
		t.Fatalf("detail = %q, want the state and the peer's words", transfers[0].Detail)
	}
}

func TestCancelDownloadRequiresAPeerAndATransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("slskd was asked to cancel nothing in particular")
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	for name, testCase := range map[string]struct{ username, transfer string }{
		"no peer":     {username: "  ", transfer: "t1"},
		"no transfer": {username: "peer", transfer: "  "},
	} {
		t.Run(name, func(t *testing.T) {
			if err := client.CancelDownload(context.Background(),
				testCase.username, testCase.transfer); err == nil {
				t.Error("slskd was asked anyway")
			}
		})
	}
}

func TestCancelDownloadStopsATransferSlskdIsRunning(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method, path = request.Method, request.URL.Path
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := testClient(t, server.URL).CancelDownload(context.Background(), "peer", "t1"); err != nil {
		t.Fatalf("error = %v, want the transfer stopped", err)
	}
	if method != http.MethodDelete || path != apiPrefix+"/transfers/downloads/peer/t1" {
		t.Fatalf("%s %s", method, path)
	}
}

// A cancellation slskd refused leaves the transfer running, and saying it
// stopped would have Schall wait for a download nobody is watching.
func TestACancellationSlskdRefusedIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := testClient(t, server.URL).CancelDownload(context.Background(), "peer", "t1")

	if err == nil || !strings.Contains(err.Error(), "slskd returned HTTP 500") {
		t.Fatalf("error = %v, want the refusal reported", err)
	}
}

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(Options{BaseURL: baseURL, APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
