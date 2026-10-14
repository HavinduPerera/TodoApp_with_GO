// Package todo_test is an external test package. It has to be: these tests use
// memstore as the Store, memstore imports todo, and a test inside package todo
// that imported memstore back would be an import cycle. The external package
// also means the tests see only what todo exports, like any other caller.
package todo_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"todoapp/internal/memstore"
	"todoapp/internal/todo"
)

var fixed = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// newService returns a Service over a fresh in-memory store with its clock
// pinned, so timestamps are exact values rather than "roughly now".
func newService(t *testing.T) *todo.Service {
	t.Helper()
	svc := todo.NewService(memstore.New())
	svc.Now = func() time.Time { return fixed }
	return svc
}

// validationFields extracts the per-field reasons from err, or fails the test.
func validationFields(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *todo.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v (%T), want *todo.ValidationError", err, err)
	}
	return ve.Fields
}

func TestCreateTrimsAndStamps(t *testing.T) {
	svc := newService(t)
	got, err := svc.Create(t.Context(), todo.CreateParams{Title: "  Buy milk  "})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID <= 0 {
		t.Errorf("ID = %d, want > 0", got.ID)
	}
	if got.Title != "Buy milk" {
		t.Errorf("Title = %q, want trimmed %q", got.Title, "Buy milk")
	}
	if got.Done {
		t.Error("Done = true for a new todo, want false")
	}
	// time.Equal, never ==: == also compares location and monotonic data.
	if !got.CreatedAt.Equal(fixed) || !got.UpdatedAt.Equal(fixed) {
		t.Errorf("timestamps = %v / %v, want both %v", got.CreatedAt, got.UpdatedAt, fixed)
	}
}

func TestCreateValidation(t *testing.T) {
	svc := newService(t)
	tests := []struct {
		name  string
		title string
	}{
		{name: "blank", title: "   "},
		{name: "one character too long", title: strings.Repeat("a", todo.MaxTitleRunes+1)},
		{name: "control character", title: "buy\x00milk"},
		{name: "tab inside", title: "buy\tmilk"},
		{name: "only a zero-width space", title: "\u200b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Create(t.Context(), todo.CreateParams{Title: tt.title})
			if fields := validationFields(t, err); fields["title"] == "" {
				t.Errorf("fields = %v, want a reason under %q", fields, "title")
			}
		})
	}
}

func TestCreateCountsRunesNotBytes(t *testing.T) {
	svc := newService(t)
	title := strings.Repeat("é", todo.MaxTitleRunes) // 200 characters, 400 bytes
	if _, err := svc.Create(t.Context(), todo.CreateParams{Title: title}); err != nil {
		t.Fatalf("Create(200 × é) = %v, want nil: the limit is in runes, not bytes", err)
	}
}

func TestCreateAcceptsEmojiSequences(t *testing.T) {
	svc := newService(t)
	// A family emoji is several code points glued by zero-width joiners (U+200D),
	// which are format characters; the visible-character rule must let it through.
	if _, err := svc.Create(t.Context(), todo.CreateParams{Title: "Call \U0001F468\u200D\U0001F469\u200D\U0001F467"}); err != nil {
		t.Fatalf("Create(emoji sequence) = %v, want nil", err)
	}
}

func TestUpdateAppliesOnlySuppliedFields(t *testing.T) {
	svc := newService(t)
	created, err := svc.Create(t.Context(), todo.CreateParams{Title: "Buy milk"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	later := fixed.Add(time.Minute)
	svc.Now = func() time.Time { return later }
	done := true
	got, err := svc.Update(t.Context(), created.ID, todo.UpdateParams{Done: &done})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !got.Done {
		t.Error("Done = false after setting it, want true")
	}
	if got.Title != created.Title {
		t.Errorf("Title = %q, want unchanged %q", got.Title, created.Title)
	}
	if !got.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, later)
	}
	if !got.CreatedAt.Equal(fixed) {
		t.Errorf("CreatedAt = %v, want unchanged %v", got.CreatedAt, fixed)
	}
}

func TestUpdateRejectsAnEmptyPatch(t *testing.T) {
	svc := newService(t)
	_, err := svc.Update(t.Context(), 1, todo.UpdateParams{})
	if fields := validationFields(t, err); fields["body"] == "" {
		t.Errorf("fields = %v, want a reason under %q", fields, "body")
	}
}

func TestUpdateValidatesTitle(t *testing.T) {
	svc := newService(t)
	blank := "   "
	_, err := svc.Update(t.Context(), 1, todo.UpdateParams{Title: &blank})
	if fields := validationFields(t, err); fields["title"] == "" {
		t.Errorf("fields = %v, want a reason under %q", fields, "title")
	}
}

func TestUpdateUnknownIDIsNotFound(t *testing.T) {
	svc := newService(t)
	done := true
	_, err := svc.Update(t.Context(), 99, todo.UpdateParams{Done: &done})
	// The Service wraps the store's error with %w; errors.Is still sees through it.
	if !errors.Is(err, todo.ErrNotFound) {
		t.Fatalf("error = %v, want one that Is todo.ErrNotFound", err)
	}
}

func TestDeleteTwiceIsNotFound(t *testing.T) {
	svc := newService(t)
	// Create what the test needs rather than relying on seeded data: the
	// fixture changes in milestone 8 and the rule under test must not care.
	created, err := svc.Create(t.Context(), todo.CreateParams{Title: "Throw me away"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(t.Context(), created.ID); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := svc.Delete(t.Context(), created.ID); !errors.Is(err, todo.ErrNotFound) {
		t.Fatalf("second Delete error = %v, want todo.ErrNotFound", err)
	}
	if _, err := svc.Get(t.Context(), created.ID); !errors.Is(err, todo.ErrNotFound) {
		t.Fatalf("Get after Delete error = %v, want todo.ErrNotFound", err)
	}
}

func TestListFillsTheDefaultLimit(t *testing.T) {
	svc := newService(t)
	page, err := svc.List(t.Context(), todo.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Limit != todo.DefaultLimit {
		t.Errorf("Limit = %d, want %d", page.Limit, todo.DefaultLimit)
	}
	if page.Items == nil {
		t.Error("Items is nil; the Page contract says it never is")
	}
}
