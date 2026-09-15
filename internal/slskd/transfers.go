package slskd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/pxldi/schall/internal/sources"
)

// maxTransferFiles bounds one enqueue request. A folder larger than this is not
// a release, and slskd should not be handed an unbounded list.
const maxTransferFiles = 500

// StartDownloads asks slskd to enqueue every file of one folder from one peer.
// The remote path is sent exactly as the peer reported it, because Soulseek
// serves the name it advertised and nothing else.
func (client *Client) StartDownloads(ctx context.Context, username string, files []sources.File) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("a peer username is required")
	}
	if len(files) == 0 {
		return errors.New("at least one file is required")
	}
	if len(files) > maxTransferFiles {
		return fmt.Errorf("%d files is more than one request may enqueue", len(files))
	}

	body := make([]transferRequest, 0, len(files))
	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			return errors.New("a remote file path is required to start a transfer")
		}
		body = append(body, transferRequest{Filename: path, Size: file.SizeBytes})
	}
	if err := client.post(ctx, "/transfers/downloads/"+url.PathEscape(username), body, nil); err != nil {
		return client.describe(ctx, err)
	}
	return nil
}

// Transfers reports what slskd is doing with this peer's files. A peer with no
// transfers at all is reported as an empty list rather than as a failure,
// because slskd answers 404 once it has forgotten the peer.
func (client *Client) Transfers(ctx context.Context, username string) ([]sources.Transfer, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("a peer username is required")
	}

	var payload transfersResponse
	err := client.get(ctx, "/transfers/downloads/"+url.PathEscape(username), nil, &payload)
	var httpError *HTTPError
	if errors.As(err, &httpError) && httpError.StatusCode == 404 {
		return []sources.Transfer{}, nil
	}
	if err != nil {
		return nil, client.describe(ctx, err)
	}

	transfers := make([]sources.Transfer, 0, len(payload.Files))
	for _, file := range payload.Files {
		transfers = append(transfers, sources.Transfer{
			ID:               file.ID,
			Path:             file.Filename,
			State:            normalizeTransferState(file.State),
			Detail:           describeTransfer(file),
			SizeBytes:        file.Size,
			TransferredBytes: file.BytesTransferred,
			Deferred:         peerDeferred(file.State, file.Exception),
		})
	}
	return transfers, nil
}

// CancelDownload stops one transfer. The downloaded part is left in place for
// slskd to deal with, because Schall does not own that directory.
func (client *Client) CancelDownload(ctx context.Context, username, transferID string) error {
	username, transferID = strings.TrimSpace(username), strings.TrimSpace(transferID)
	if username == "" || transferID == "" {
		return errors.New("a peer username and transfer ID are required")
	}
	endpoint := "/transfers/downloads/" + url.PathEscape(username) + "/" + url.PathEscape(transferID)
	request, err := client.newRequest(ctx, "DELETE", endpoint, nil, nil)
	if err != nil {
		return err
	}
	if err := client.do(request, nil); err != nil {
		var httpError *HTTPError
		// A transfer slskd has already forgotten is not running, which is what
		// cancelling was for.
		if errors.As(err, &httpError) && httpError.StatusCode == 404 {
			return nil
		}
		return client.describe(ctx, err)
	}
	return nil
}

// normalizeTransferState reduces a slskd state, which is a comma-joined set
// such as "Completed, Succeeded", to the vocabulary Schall reasons about. An
// unrecognised state is treated as still queued rather than as finished, so an
// unknown word can never make Schall believe a file arrived.
func normalizeTransferState(state string) string {
	state = strings.ToLower(state)
	switch {
	case strings.Contains(state, "succeeded"):
		return sources.TransferCompleted
	case strings.Contains(state, "cancelled"), strings.Contains(state, "canceled"):
		return sources.TransferCancelled
	case strings.Contains(state, "errored"), strings.Contains(state, "timedout"),
		strings.Contains(state, "rejected"), strings.Contains(state, "aborted"):
		return sources.TransferFailed
	case strings.Contains(state, "inprogress"):
		return sources.TransferDownloading
	case strings.Contains(state, "completed"):
		// Completed without a recognised outcome word: something finished, but
		// nothing here proves it arrived intact.
		return sources.TransferFailed
	default:
		return sources.TransferQueued
	}
}

// peerQueueLimits are the two things a Soulseek client says when it is refusing
// for load rather than refusing outright. Both are counted against the person
// asking: "too many files" is how many of that peer's files we already have
// queued with them, and "too many megabytes" is the same limit measured in
// bytes. SoulseekQt and Nicotine+ both send these words. "Overwhelmed with
// requests" is slskd's own wording for the same load limit. None of the
// three says anything about the file — the peer is sharing it, and will send
// it once we stop asking for so much of it at once.
//
// Nothing else is on this list, and deliberately. "File not shared" and
// "Banned" are refusals about the file and about us; a reason nobody here
// recognises is read as a plain failure, because the cost of mistaking a real
// refusal for backpressure is a want that asks the same peer forever, while the
// cost of the reverse is one copy rested for a day.
var peerQueueLimits = []string{"too many files", "too many megabytes", "overwhelmed with requests"}

// peerDeferred reports a rejection that means "not now" rather than "no".
//
// Both halves have to agree: the state has to say the peer rejected the
// transfer, and the reason it gave has to name one of its queue limits. A
// transfer that failed some other way is never read as backpressure however its
// exception happens to be worded.
func peerDeferred(state, exception string) bool {
	if !strings.Contains(strings.ToLower(state), "rejected") {
		return false
	}
	reason := strings.ToLower(exception)
	for _, limit := range peerQueueLimits {
		if strings.Contains(reason, limit) {
			return true
		}
	}
	return false
}

func describeTransfer(file transferFile) string {
	detail := strings.TrimSpace(file.State)
	if message := strings.TrimSpace(file.Exception); message != "" {
		detail = strings.TrimSpace(detail + ": " + message)
	}
	const maxDetail = 500
	if len(detail) > maxDetail {
		detail = detail[:maxDetail]
	}
	return detail
}

type transferRequest struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type transferFile struct {
	ID               string `json:"id"`
	Filename         string `json:"filename"`
	State            string `json:"state"`
	Size             int64  `json:"size"`
	BytesTransferred int64  `json:"bytesTransferred"`
	Exception        string `json:"exception"`
}

// transfersResponse flattens the shape slskd returns. Real builds group a
// peer's transfers by directory, so the files are collected from there, but a
// flat list and a bare array are both accepted: the version field taught this
// project that guessing one shape and testing the same guess proves nothing.
type transfersResponse struct {
	Files []transferFile
}

func (response *transfersResponse) UnmarshalJSON(data []byte) error {
	var flat []transferFile
	if err := json.Unmarshal(data, &flat); err == nil {
		response.Files = flat
		return nil
	}
	var object struct {
		Files       []transferFile `json:"files"`
		Directories []struct {
			Files []transferFile `json:"files"`
		} `json:"directories"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return fmt.Errorf("decode slskd transfers: %w", err)
	}
	response.Files = object.Files
	for _, directory := range object.Directories {
		response.Files = append(response.Files, directory.Files...)
	}
	return nil
}
