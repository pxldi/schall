package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/transcode"
)

// importSettingsResponse reports the policy together with whether Schall could
// act on it. The two are separate answers on purpose: a stored policy that the
// filesystem will refuse is exactly the situation this view exists to make
// visible.
type importSettingsResponse struct {
	SourceRetention string `json:"sourceRetention"`
	InboxPath       string `json:"inboxPath,omitempty"`
	InboxWritable   bool   `json:"inboxWritable"`
	InboxDetail     string `json:"inboxDetail,omitempty"`
	AcoustIDKeySet  bool   `json:"acoustidKeySet"`
	AcoustIDEnabled bool   `json:"acoustidEnabled"`
	// The re-encoding policy, and whether an encoder is installed to carry it
	// out. The second is separate for the same reason InboxWritable is: a
	// stored policy the machine will not perform is what this view exists to
	// show.
	TranscodeEnabled  bool       `json:"transcodeEnabled"`
	TranscodeTarget   string     `json:"transcodeTarget"`
	TranscodeBitrate  string     `json:"transcodeBitrate"`
	TranscodeWhen     string     `json:"transcodeWhen"`
	TranscoderPresent bool       `json:"transcoderPresent"`
	TranscoderDetail  string     `json:"transcoderDetail,omitempty"`
	UpdatedAt         *time.Time `json:"updatedAt,omitempty"`
}

func (api *API) importSettingsResponse(row db.ImportSettingsRow) importSettingsResponse {
	response := importSettingsResponse{
		SourceRetention: row.SourceRetention,
		InboxPath:       api.inboxPath,
		// The key itself never leaves the process, exactly as the slskd one
		// does not. Callers learn only whether one is stored.
		AcoustIDKeySet:   strings.TrimSpace(row.AcoustIDAPIKey) != "",
		AcoustIDEnabled:  row.AcoustIDEnabled,
		TranscodeEnabled: row.TranscodeEnabled,
		TranscodeTarget:  row.TranscodeTarget,
		TranscodeBitrate: row.TranscodeBitrate,
		TranscodeWhen:    row.TranscodeWhen,
		UpdatedAt:        nullableTime(row.UpdatedAt.Valid, row.UpdatedAt.Time),
	}
	if err := api.encoderPresent(); err != nil {
		response.TranscoderDetail = err.Error()
	} else {
		response.TranscoderPresent = true
	}
	if err := writableInbox(api.inboxPath); err != nil {
		response.InboxDetail = err.Error()
		return response
	}
	response.InboxWritable = true
	return response
}

func (api *API) getImportSettings(response http.ResponseWriter, request *http.Request) {
	if !api.settingsAvailable(response) {
		return
	}
	row, err := api.settings.ImportSettings(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, api.importSettingsResponse(row))
}

type importSettingsRequest struct {
	// SourceRetention is what Schall does with the provider's copy of a release
	// it has imported. Removal is destructive and is never a side effect of
	// anything else, which is why it is set here and nowhere else.
	SourceRetention string `json:"sourceRetention"`
	// AcoustIDAPIKey is empty when the caller is not changing it. The key is
	// never sent back to the interface, so it cannot echo it to keep it.
	AcoustIDAPIKey  string `json:"acoustidApiKey"`
	AcoustIDEnabled bool   `json:"acoustidEnabled"`
	// TranscodeEnabled asks for higher formats to be written into the library
	// as the target format. It is lossy and it is permanent: the copy the
	// provider had is not kept, and the detail thrown away cannot be recovered.
	//
	// All four transcode fields are pointers, and all four for the same
	// reason: a caller that means to change only the source retention or the
	// acoustic check sends no opinion about re-encoding at all, and a bare
	// bool or an empty string cannot be told apart from "turn it off" or
	// "clear it". Omitted (nil) leaves the stored policy exactly as it was.
	TranscodeEnabled *bool   `json:"transcodeEnabled"`
	TranscodeTarget  *string `json:"transcodeTarget"`
	TranscodeBitrate *string `json:"transcodeBitrate"`
	TranscodeWhen    *string `json:"transcodeWhen"`
}

func (api *API) saveImportSettings(response http.ResponseWriter, request *http.Request) {
	if !api.settingsAvailable(response) {
		return
	}
	var input importSettingsRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}

	retention := strings.TrimSpace(input.SourceRetention)
	if retention != db.SourceRetentionKeep && retention != db.SourceRetentionDelete {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{`sourceRetention must be "keep" or "delete"`})
		return
	}
	// A policy Schall could not carry out is refused rather than stored. The
	// download inbox is normally mounted read-only, which is a sensible default
	// and a poor surprise: accepting the policy here would mean every release it
	// was set for reporting a refusal later, one at a time.
	if retention == db.SourceRetentionDelete {
		if err := writableInbox(api.inboxPath); err != nil {
			api.problem(response, http.StatusUnprocessableEntity,
				"the download inbox cannot be written to",
				[]string{err.Error(), "Mount the completed-download folder writable to remove imported files."})
			return
		}
	}

	// Read once and used twice: as the fallback for whichever of the acoustic
	// check and the transcode policy this request has no opinion about, and as
	// what the acoustic check's key requirement is checked against.
	stored, err := api.settings.ImportSettings(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	// Enabling the acoustic check without a key would look like a working
	// verification that silently verifies nothing, which is worse than an
	// honestly absent one. The database refuses it too; this says why.
	key := strings.TrimSpace(input.AcoustIDAPIKey)
	if input.AcoustIDEnabled && key == "" && strings.TrimSpace(stored.AcoustIDAPIKey) == "" {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"an AcoustID API key is required to verify imports acoustically"})
		return
	}

	// The policy that will be written is the stored one with only the fields
	// this request has an opinion about replaced — never a bare
	// "input.TranscodeEnabled", because a request that never mentions
	// re-encoding must not silently turn a stored policy off. It is checked
	// before it is stored, and enabling it is refused on a machine with no
	// encoder: a setting that is on and does nothing is the worst of the three
	// states, because the operator believes their disc is filling slower than
	// it is.
	policy := transcode.Policy{
		Enabled: stored.TranscodeEnabled,
		Target:  stored.TranscodeTarget,
		Bitrate: stored.TranscodeBitrate,
		When:    stored.TranscodeWhen,
	}
	if input.TranscodeEnabled != nil {
		policy.Enabled = *input.TranscodeEnabled
	}
	if input.TranscodeTarget != nil {
		if target := strings.TrimSpace(*input.TranscodeTarget); target != "" {
			policy.Target = target
		}
	}
	if input.TranscodeBitrate != nil {
		if bitrate := strings.TrimSpace(*input.TranscodeBitrate); bitrate != "" {
			policy.Bitrate = bitrate
		}
	}
	if input.TranscodeWhen != nil {
		if when := strings.TrimSpace(*input.TranscodeWhen); when != "" {
			policy.When = when
		}
	}
	// A row with nothing stored yet -- a fresh installation, or one from
	// before these columns existed -- has blank fields here rather than the
	// database's own defaults, which only apply to a row actually written.
	// Filled in the same way transcode.Default() would answer, so a request
	// that never mentions re-encoding validates against a real policy rather
	// than an empty one.
	if policy.Target == "" {
		policy.Target = transcode.TargetMP3
	}
	if policy.Bitrate == "" {
		policy.Bitrate = transcode.Default().Bitrate
	}
	if policy.When == "" {
		policy.When = transcode.WhenLossless
	}
	if err := policy.Validate(); err != nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{err.Error()})
		return
	}
	if policy.Enabled {
		if err := api.encoderPresent(); err != nil {
			api.problem(response, http.StatusUnprocessableEntity,
				"music cannot be re-encoded on this machine",
				[]string{err.Error(), "Install ffmpeg, or leave the files as they arrive."})
			return
		}
	}

	row, err := api.settings.SaveImportSettings(request.Context(), db.SaveImportSettingsParams{
		SourceRetention:  retention,
		AcoustIDAPIKey:   key,
		AcoustIDEnabled:  input.AcoustIDEnabled,
		TranscodeEnabled: &policy.Enabled,
		TranscodeTarget:  policy.Target,
		TranscodeBitrate: policy.Bitrate,
		TranscodeWhen:    policy.When,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("source_retention", row.SourceRetention).Msg("import retention policy saved")
	api.writeJSON(response, http.StatusOK, api.importSettingsResponse(row))
}

// encoderPresent reports whether this machine can re-encode music at all, by
// looking for the binary rather than by running it. A path that was configured
// is checked as a path; an empty one is looked for on PATH, which is where the
// shipped image puts it.
func (api *API) encoderPresent() error {
	binary := strings.TrimSpace(api.encoderPath)
	if binary == "" {
		binary = "ffmpeg"
	}
	if strings.ContainsRune(binary, os.PathSeparator) {
		info, err := os.Stat(binary)
		if err != nil {
			return fmt.Errorf("%s could not be found", binary)
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a folder, not a program", binary)
		}
		return nil
	}
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("%s could not be found", binary)
	}
	return nil
}

// writableInbox reports whether Schall can remove anything from the download
// inbox, by writing there rather than by reading permissions: a read-only bind
// mount grants the bits and refuses the write anyway. The probe file is removed
// immediately and is the only thing Schall ever creates in the provider folder.
func writableInbox(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("no completed-download folder is configured")
	}
	probe, err := os.CreateTemp(path, ".schall-retention-")
	if err != nil {
		// The probe's generated name says nothing useful; the folder and the
		// reason it refused are the whole answer.
		reason := err.Error()
		var problem *os.PathError
		if errors.As(err, &problem) {
			reason = problem.Err.Error()
		}
		return fmt.Errorf("the completed-download folder %s is not writable: %s", path, reason)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}
