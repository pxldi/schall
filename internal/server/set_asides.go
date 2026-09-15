package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
)

func (api *API) setLibraryFileAside(response http.ResponseWriter, request *http.Request) {
	api.setLibraryFileAsideState(response, request, true)
}

func (api *API) clearLibraryFileSetAside(response http.ResponseWriter, request *http.Request) {
	api.setLibraryFileAsideState(response, request, false)
}

func (api *API) setLibraryFileAsideState(
	response http.ResponseWriter, request *http.Request, setAside bool,
) {
	if !api.libraryAvailable(response) {
		return
	}
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if setAside {
		err = api.library.SetLibraryFileAside(request.Context(), fileID)
	} else {
		err = api.library.ClearLibraryFileSetAside(request.Context(), fileID)
	}
	switch {
	case errors.Is(err, db.ErrLibraryFileNotFound):
		api.problem(response, http.StatusNotFound, "library file not found", nil)
	case errors.Is(err, db.ErrLibraryFileNotSetAside):
		api.problem(response, http.StatusNotFound, "library file is not set aside", nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.decisions.Publish()
		response.WriteHeader(http.StatusNoContent)
	}
}
