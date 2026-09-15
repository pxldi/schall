package activity

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func nullTarget() uuid.NullUUID { return uuid.NullUUID{} }

func at(minutes int) time.Time {
	return time.Date(2026, 8, 28, 20, minutes, 0, 0, time.UTC)
}

// The evening this was written, one sweep re-encoded five hundred and
// forty-three files in twenty-three minutes. Five hundred and forty-three rows
// saying the same sentence is not a story anybody reads, and the four events
// worth seeing would have been under all of them.
func TestARunOfTheSameThingIsOneLineWithItsCount(t *testing.T) {
	events := collapse([]Event{
		{Kind: Removed, At: at(57), Since: at(57), Count: 1},
		{Kind: Removed, At: at(50), Since: at(50), Count: 1},
		{Kind: Removed, At: at(34), Since: at(34), Count: 1},
	})

	if len(events) != 1 {
		t.Fatalf("rows = %d, want one", len(events))
	}
	if events[0].Count != 3 {
		t.Errorf("count = %d, want 3", events[0].Count)
	}
	// Read newest first, so the run ends at the newest and starts at the oldest.
	if !events[0].At.Equal(at(57)) || !events[0].Since.Equal(at(34)) {
		t.Errorf("run ran %v to %v", events[0].Since, events[0].At)
	}
}

// Neighbouring, not all of a kind. Two arrivals with a refusal between them is
// what the evening actually looked like, and folding them together would be
// the feed rewriting it.
func TestTwoRunsOfOneKindWithSomethingBetweenThemStayApart(t *testing.T) {
	events := collapse([]Event{
		{Kind: Arrived, At: at(50), Since: at(50), Count: 1},
		{Kind: Refused, At: at(40), Since: at(40), Count: 1},
		{Kind: Arrived, At: at(30), Since: at(30), Count: 1},
	})

	if len(events) != 3 {
		t.Fatalf("rows = %d, want three", len(events))
	}
	for index, want := range []Kind{Arrived, Refused, Arrived} {
		if events[index].Kind != want {
			t.Errorf("row %d = %q, want %q", index, events[index].Kind, want)
		}
	}
}

// A feed longer than the screen is a page of history, which is not what this
// answers. The cap is applied after runs are folded, so a burst cannot spend it.
func TestTheFeedStopsAtItsLimit(t *testing.T) {
	var raw []Event
	for index := 0; index < Limit*2; index++ {
		kind := Arrived
		if index%2 == 1 {
			kind = Refused
		}
		raw = append(raw, Event{Kind: kind, At: at(59 - index), Count: 1})
	}

	if got := len(collapse(raw)); got != Limit {
		t.Fatalf("rows = %d, want %d", got, Limit)
	}
}

// Only a question leads anywhere. An arrival and a refusal are settled, and a
// link onto a page that says "nothing to decide" is a link that wasted a press.
func TestOnlyAQuestionCarriesSomewhereToGo(t *testing.T) {
	if got := href(Arrived, nullTarget()); got != "" {
		t.Errorf("an arrival links to %q", got)
	}
	if got := href(Asked, nullTarget()); got != "" {
		t.Errorf("a question with no want links to %q", got)
	}
}
