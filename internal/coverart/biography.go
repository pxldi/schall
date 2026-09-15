package coverart

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/wikipedia"
)

// ErrNoBiography reports that no encyclopaedia has heard of this artist. It is
// an answer about the artist rather than a failure, so a caller can record it
// and stop asking.
var ErrNoBiography = errors.New("no encyclopaedia has an article about this artist")

// englishWikipedia is the sitelink read off a Wikidata entity. One language,
// because the biography is shown in one language and the interface is English.
const englishWikipedia = "enwiki"

// Biography is a few lines about an artist, and the article they came from.
//
// SourceURL is not decoration. Wikipedia text is CC BY-SA, so whatever shows
// the words has to name Wikipedia and link the article; a biography arrives
// with its link or it does not arrive.
type Biography struct {
	Text      string
	SourceURL string
}

// Encyclopaedia is the part of Wikipedia this needs: the opening of one article,
// asked for by its exact title.
type Encyclopaedia interface {
	Summary(context.Context, string) (wikipedia.Summary, error)
}

// Biographies answers "who is this artist" for the artist page.
//
// It is the artist-picture chain with one more hop, and it is identifier-keyed
// in exactly the same way. MusicBrainz says which Wikidata entity this artist
// is. The entity says which Wikipedia article is about it. The article says who
// they are. Three services, each making a statement about the entity the one
// before named, and no name is sent to any of them.
//
// Only artists an encyclopaedia has heard of get words, which is the same
// honest limit the picture chain has. Nothing is asked of a service that would
// answer a name — a biography found by searching for "Marram" would be somebody
// else's life under the right picture.
type Biographies struct {
	relations     ImageRelations
	entities      *Client
	encyclopaedia Encyclopaedia
}

func NewBiographies(
	relations ImageRelations, entities *Client, encyclopaedia Encyclopaedia,
) *Biographies {
	return &Biographies{relations: relations, entities: entities, encyclopaedia: encyclopaedia}
}

// Of returns a few lines about the artist, or ErrNoBiography when no article
// names them.
func (biographies *Biographies) Of(ctx context.Context, artistID uuid.UUID) (Biography, error) {
	if artistID == uuid.Nil {
		return Biography{}, ErrNoBiography
	}
	found, err := biographies.relations.ArtistURLRelations(ctx, artistID)
	if err != nil {
		return Biography{}, err
	}
	for _, relation := range found {
		if relation.Type != "wikidata" {
			continue
		}
		title, err := biographies.article(ctx, relation.Resource)
		if errors.Is(err, ErrNoBiography) {
			continue
		}
		if err != nil {
			return Biography{}, err
		}
		summary, err := biographies.encyclopaedia.Summary(ctx, title)
		if errors.Is(err, wikipedia.ErrNoArticle) {
			continue
		}
		if err != nil {
			return Biography{}, err
		}
		return Biography{Text: summary.Text, SourceURL: summary.URL}, nil
	}
	return Biography{}, ErrNoBiography
}

// article asks Wikidata which Wikipedia article is about this entity. The entity
// is the one MusicBrainz named, and the article is the one Wikidata names: two
// statements, no guessing between them.
func (biographies *Biographies) article(ctx context.Context, resource string) (string, error) {
	entity, ok := wikidataEntityID(resource)
	if !ok {
		return "", ErrNoBiography
	}
	response, err := biographies.entities.get(ctx, biographies.entities.httpClient, wikidataEntity+entity+".json")
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxImageBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode == http.StatusNotFound {
		return "", ErrNoBiography
	}
	if response.StatusCode != http.StatusOK {
		return "", errors.New("Wikidata returned " + response.Status)
	}
	var payload wikidataSitelinks
	if err := json.NewDecoder(io.LimitReader(response.Body, maxImageBytes)).Decode(&payload); err != nil {
		return "", err
	}
	title := strings.TrimSpace(payload.Entities[entity].Sitelinks[englishWikipedia].Title)
	if title == "" {
		// The entity exists and no English article is about it. That is an
		// answer, and the artist is recorded as asked.
		return "", ErrNoBiography
	}
	return title, nil
}

// wikidataSitelinks is one entity as Wikidata prints it, read for its sitelinks
// alone. It is a second narrow view of the same document rather than a field
// added to wikidataEntities, for the reason that one gives: a document with
// claims of every shape is only safe to decode one statement at a time.
type wikidataSitelinks struct {
	Entities map[string]struct {
		Sitelinks map[string]struct {
			Title string `json:"title"`
		} `json:"sitelinks"`
	} `json:"entities"`
}
