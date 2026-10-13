package accuraterip

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// chunk is one disc as AccurateRip's files hold it: track count, the three
// IDs, then confidence, checksum and frame-450 checksum per track.
func chunk(ids IDs, submissions ...Submission) []byte {
	body := []byte{byte(len(submissions))}
	body = binary.LittleEndian.AppendUint32(body, ids.ID1)
	body = binary.LittleEndian.AppendUint32(body, ids.ID2)
	body = binary.LittleEndian.AppendUint32(body, ids.CDDB)
	for _, submission := range submissions {
		body = append(body, byte(submission.Confidence))
		body = binary.LittleEndian.AppendUint32(body, submission.Checksum)
		body = binary.LittleEndian.AppendUint32(body, 0)
	}
	return body
}

func serve(t *testing.T, status int, body []byte) (*Client, *string) {
	t.Helper()
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asked = request.URL.Path
		response.WriteHeader(status)
		_, _ = response.Write(body)
	}))
	t.Cleanup(server.Close)
	return NewClient(Options{BaseURL: server.URL}), &asked
}

func TestPressingsReadsEveryPressingOfTheDisc(t *testing.T) {
	ids := IDs{Tracks: 2, ID1: 1, ID2: 2, CDDB: 3}
	body := append(chunk(ids, Submission{5, 0xaa}, Submission{5, 0xbb}),
		chunk(ids, Submission{1, 0xcc}, Submission{1, 0xdd})...)
	client, asked := serve(t, http.StatusOK, body)

	pressings, err := client.Pressings(context.Background(), ids)
	if err != nil {
		t.Fatalf("Pressings() error = %v", err)
	}
	if len(pressings) != 2 || pressings[1].Tracks[1] != (Submission{1, 0xdd}) {
		t.Errorf("pressings = %+v", pressings)
	}
	if *asked != ids.path() {
		t.Errorf("asked %q, want %q", *asked, ids.path())
	}
}

func TestPressingsListedUnderAnotherDiscAreNotRead(t *testing.T) {
	ids := IDs{Tracks: 2, ID1: 1, ID2: 2, CDDB: 3}
	other := IDs{Tracks: 2, ID1: 1, ID2: 2, CDDB: 4}
	client, _ := serve(t, http.StatusOK, chunk(other, Submission{5, 0xaa}, Submission{5, 0xbb}))

	if _, err := client.Pressings(context.Background(), ids); !errors.Is(err, ErrNotListed) {
		t.Fatalf("Pressings() error = %v, want ErrNotListed", err)
	}
}

func TestADiscNobodySubmittedIsNotListed(t *testing.T) {
	client, _ := serve(t, http.StatusNotFound, nil)
	if _, err := client.Pressings(context.Background(), IDs{Tracks: 3}); !errors.Is(err, ErrNotListed) {
		t.Fatalf("Pressings() error = %v, want ErrNotListed", err)
	}
}

func TestAnAnswerCutInsideADiscIsAnError(t *testing.T) {
	ids := IDs{Tracks: 2, ID1: 1, ID2: 2, CDDB: 3}
	body := chunk(ids, Submission{5, 0xaa}, Submission{5, 0xbb})
	client, _ := serve(t, http.StatusOK, body[:len(body)-1])

	_, err := client.Pressings(context.Background(), ids)
	if err == nil || errors.Is(err, ErrNotListed) {
		t.Fatalf("Pressings() error = %v, want a failure", err)
	}
}
