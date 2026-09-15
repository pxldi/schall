package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/library"
	"github.com/rs/zerolog"
)

// layoutStore is the fakeStore with the layout on top, so NewAPI's interface
// assertion finds it. stored is false until something departs from the
// default, which is the state a fresh installation is in.
type layoutStore struct {
	*fakeStore
	template string
	stored   bool
	err      error
}

func (store *layoutStore) LibraryLayoutSettings(context.Context) (db.LibraryLayoutRow, error) {
	if store.err != nil {
		return db.LibraryLayoutRow{}, store.err
	}
	if !store.stored {
		return db.LibraryLayoutRow{}, pgx.ErrNoRows
	}
	return db.LibraryLayoutRow{Template: store.template}, nil
}

func (store *layoutStore) SaveLibraryLayoutSettings(
	_ context.Context, template string,
) (db.LibraryLayoutRow, error) {
	if store.err != nil {
		return db.LibraryLayoutRow{}, store.err
	}
	store.template, store.stored = template, true
	return db.LibraryLayoutRow{Template: template}, nil
}

func layoutHandler(store *layoutStore) http.Handler {
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
}

func readLayout(t *testing.T, body *strings.Reader, handler http.Handler, method string) libraryLayoutResponse {
	t.Helper()
	response := httptest.NewRecorder()
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, "/api/v1/settings/library-layout", nil)
	} else {
		request = httptest.NewRequest(method, "/api/v1/settings/library-layout", body)
	}
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var decoded libraryLayoutResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s: %v", response.Body, err)
	}
	return decoded
}

// An installation that has never opened this setting still has a layout. What
// it does not have is a row, and answering 404 for it would say the library is
// filed nowhere.
func TestTheLayoutIsReadableBeforeAnythingIsStored(t *testing.T) {
	decoded := readLayout(t, nil, layoutHandler(&layoutStore{fakeStore: &fakeStore{}}), http.MethodGet)

	if decoded.Template != library.DefaultTemplate {
		t.Fatalf("template = %q, want the default %q", decoded.Template, library.DefaultTemplate)
	}
	if !decoded.IsDefault {
		t.Fatal("isDefault = false, want the untouched setting reported as the default")
	}
	if decoded.UpdatedAt != nil {
		t.Fatalf("updatedAt = %v, want nothing recorded as having been changed", decoded.UpdatedAt)
	}
}

// The example is rendered by the code an import would use, so what it shows is
// where a release actually lands rather than a description of it.
func TestTheLayoutIsShownWithWhereItWouldFileARelease(t *testing.T) {
	decoded := readLayout(t, nil, layoutHandler(&layoutStore{fakeStore: &fakeStore{}}), http.MethodGet)

	if want := "Boards of Canada/Geogaddi [a3f19c2b]"; decoded.Example != want {
		t.Fatalf("example = %q, want %q", decoded.Example, want)
	}
}

// Saving proves the template renders before it is stored, so the refusal names
// the word that was wrong instead of arriving as a constraint violation.
func TestSavingATemplateThatNamesAnUnknownFieldIsRefused(t *testing.T) {
	store := &layoutStore{fakeStore: &fakeStore{}}
	response := httptest.NewRecorder()
	layoutHandler(store).ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/settings/library-layout",
		strings.NewReader(`{"template":"{albumartist}/{album}"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "albumartist") {
		t.Fatalf("body = %s, want the offending word quoted back", response.Body)
	}
	if store.stored {
		t.Fatalf("stored = %q, want nothing stored", store.template)
	}
}

// A template that would write outside the library is refused for the same
// reason and in the same place.
func TestSavingATemplateThatClimbsOutOfTheLibraryIsRefused(t *testing.T) {
	store := &layoutStore{fakeStore: &fakeStore{}}
	response := httptest.NewRecorder()
	layoutHandler(store).ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/settings/library-layout",
		strings.NewReader(`{"template":"../{artist}/{album}"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.stored {
		t.Fatalf("stored = %q, want nothing stored", store.template)
	}
}

// A template that survives the parser is stored as it was written, and the
// answer shows where it would now file a release.
func TestSavingATemplateStoresItAndShowsWhatItFiles(t *testing.T) {
	store := &layoutStore{fakeStore: &fakeStore{}}
	decoded := readLayout(t,
		strings.NewReader(`{"template":"{artist}/{album}"}`),
		layoutHandler(store), http.MethodPut)

	if store.template != "{artist}/{album}" {
		t.Fatalf("stored = %q, want the template as written", store.template)
	}
	if decoded.IsDefault {
		t.Fatal("isDefault = true, want a departure from the default reported as one")
	}
	if want := "Boards of Canada/Geogaddi"; decoded.Example != want {
		t.Fatalf("example = %q, want %q", decoded.Example, want)
	}
}

// fakeMover stands in for the component that migrates the library. It records
// what it was asked to do, because whether the renames were asked for at all is
// the contract these handlers have.
type fakeMover struct {
	plan      library.PlanResult
	planErr   error
	run       library.RunSummary
	runErr    error
	queued    []uuid.UUID
	discarded []uuid.UUID
}

func (mover *fakeMover) Plan(context.Context) (library.PlanResult, error) {
	return mover.plan, mover.planErr
}

func (mover *fakeMover) Run(_ context.Context, runID uuid.UUID) (library.RunSummary, error) {
	if mover.runErr != nil {
		return library.RunSummary{}, mover.runErr
	}
	summary := mover.run
	summary.RunID = runID
	return summary, nil
}

func (mover *fakeMover) LatestRun(ctx context.Context) (library.RunSummary, error) {
	return mover.Run(ctx, mover.run.RunID)
}

func (mover *fakeMover) Discard(ctx context.Context, runID uuid.UUID) (library.RunSummary, error) {
	mover.discarded = append(mover.discarded, runID)
	return mover.Run(ctx, runID)
}

func (mover *fakeMover) QueueApply(_ context.Context, runID uuid.UUID) error {
	mover.queued = append(mover.queued, runID)
	return nil
}

func moverHandler(mover *fakeMover) http.Handler {
	return NewAPI(&layoutStore{fakeStore: &fakeStore{}}, fakeDatabase{}, &fakeArtistSearcher{},
		zerolog.Nop(), WithLayoutMover(mover))
}

func moverRequest(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

// A library that was never migrated is the ordinary state of one, and answering
// with nothing says so without pretending a run exists.
func TestALibraryWithNoMigrationAnswersWithNothing(t *testing.T) {
	mover := &fakeMover{runErr: library.ErrRunNotFound}
	response := moverRequest(t, moverHandler(mover), http.MethodGet, "/api/v1/library/layout/moves")

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Planning writes down what would happen and reports what it left alone, so a
// migration can be judged before it is agreed to.
func TestPlanningReportsWhatItLeavesWhereItIs(t *testing.T) {
	mover := &fakeMover{
		plan: library.PlanResult{RunID: uuid.New(), Planned: 3, Colliding: 1, InPlace: 9, Unfiled: 2},
		run:  library.RunSummary{Planned: 3, Failed: 1},
	}
	response := moverRequest(t, moverHandler(mover), http.MethodPost, "/api/v1/library/layout/moves")

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var decoded layoutRunResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Plan == nil || decoded.Plan.Unfiled != 2 || decoded.Plan.InPlace != 9 {
		t.Fatalf("plan = %#v, want what was left where it is", decoded.Plan)
	}
	if len(mover.queued) != 0 {
		t.Fatalf("queued = %v, want planning to move nothing", mover.queued)
	}
}

// A plan is a claim on every file in it, so a second one while the first is
// open is refused rather than queued behind it.
func TestPlanningIsRefusedWhileARunIsOpen(t *testing.T) {
	mover := &fakeMover{planErr: library.ErrRunOpen}
	response := moverRequest(t, moverHandler(mover), http.MethodPost, "/api/v1/library/layout/moves")

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Applying is asked for separately, because the plan is the thing that was
// agreed to.
func TestApplyingARunQueuesTheRenames(t *testing.T) {
	runID := uuid.New()
	mover := &fakeMover{run: library.RunSummary{Planned: 4}}
	response := moverRequest(t, moverHandler(mover), http.MethodPost,
		"/api/v1/library/layout/moves/"+runID.String()+"/apply")

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(mover.queued) != 1 || mover.queued[0] != runID {
		t.Fatalf("queued = %v, want %s", mover.queued, runID)
	}
}

// A run with nothing left to move has nothing to apply, and saying so is better
// than queueing a job to discover it.
func TestApplyingAFinishedRunIsRefused(t *testing.T) {
	mover := &fakeMover{run: library.RunSummary{Moved: 4}}
	response := moverRequest(t, moverHandler(mover), http.MethodPost,
		"/api/v1/library/layout/moves/"+uuid.New().String()+"/apply")

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(mover.queued) != 0 {
		t.Fatalf("queued = %v, want nothing", mover.queued)
	}
}

// Discarding drops a plan that never happened, which is what frees the files
// for one somebody would rather have.
func TestDiscardingARunDropsWhatWasNeverDone(t *testing.T) {
	runID := uuid.New()
	mover := &fakeMover{runErr: library.ErrRunNotFound}
	response := moverRequest(t, moverHandler(mover), http.MethodDelete,
		"/api/v1/library/layout/moves/"+runID.String())

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(mover.discarded) != 1 || mover.discarded[0] != runID {
		t.Fatalf("discarded = %v, want %s", mover.discarded, runID)
	}
}

// An installation with nothing wired to move files says so, rather than
// answering as though a migration had been planned.
func TestMigratingIsUnavailableWithoutAMover(t *testing.T) {
	handler := NewAPI(&layoutStore{fakeStore: &fakeStore{}}, fakeDatabase{},
		&fakeArtistSearcher{}, zerolog.Nop())
	response := moverRequest(t, handler, http.MethodPost, "/api/v1/library/layout/moves")

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A body nothing can read is not a layout to store, and it is refused in the
// shape every other handler refuses one.
func TestSavingALayoutFromABodyThatIsNotJSONIsRefused(t *testing.T) {
	store := &layoutStore{fakeStore: &fakeStore{}}
	response := httptest.NewRecorder()
	layoutHandler(store).ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/settings/library-layout", strings.NewReader(`not json`)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.stored {
		t.Fatalf("stored = %q, want nothing stored", store.template)
	}
}
