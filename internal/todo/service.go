package todo

import (
	"context"
	"fmt"
	"time"
)

// Service applies the rules of the domain: validation, timestamps and
// defaults. Handlers call it; it calls the Store. Keeping the rules here means
// every store implementation gets them for free, and two stores can never
// drift on questions like "what time is it" or "how long may a title be".
type Service struct {
	store Store

	// Now is the clock. It is an exported field rather than a constructor
	// option so a test can pin time with one assignment:
	//     svc.Now = func() time.Time { return fixed }
	// A functional-options constructor is the upgrade when a second knob appears.
	Now func() time.Time
}

// NewService accepts the interface it needs and returns the concrete type it
// builds: "accept interfaces, return structs".
func NewService(store Store) *Service {
	return &Service{
		store: store,
		// UTC and microsecond precision match what the SQLite store will persist
		// from milestone 8, so what comes back from the database is Equal to what
		// went in. UTC() also strips the monotonic clock reading.
		Now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
	}
}

// Create validates p, stamps both timestamps and stores the todo.
func (s *Service) Create(ctx context.Context, p CreateParams) (Todo, error) {
	title, reason := validateTitle(p.Title)
	if reason != "" {
		return Todo{}, &ValidationError{Fields: map[string]string{"title": reason}}
	}
	now := s.Now()
	t, err := s.store.Create(ctx, Todo{Title: title, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return Todo{}, fmt.Errorf("create todo: %w", err)
	}
	return t, nil
}

// Get returns one todo. Errors are wrapped with %w, which adds context for a
// log line while keeping ErrNotFound findable with errors.Is.
func (s *Service) Get(ctx context.Context, id int64) (Todo, error) {
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return Todo{}, fmt.Errorf("get todo %d: %w", id, err)
	}
	return t, nil
}

// List fills in the default page size and returns a Page. Range checks on the
// filter are the transport's job (milestone 11); the default is a domain decision.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	if f.Limit == 0 {
		f.Limit = DefaultLimit
	}
	items, total, err := s.store.List(ctx, f)
	if err != nil {
		return Page{}, fmt.Errorf("list todos: %w", err)
	}
	return Page{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset}, nil
}

// Update validates the supplied fields and applies them with a fresh
// UpdatedAt. An update that supplies nothing is rejected: it would change only
// the timestamp, which is never what a client meant. All problems are reported
// together in one ValidationError.
func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Todo, error) {
	fields := map[string]string{}
	if p.Title == nil && p.Done == nil {
		fields["body"] = "at least one of title or done is required"
	}
	if p.Title != nil {
		title, reason := validateTitle(*p.Title)
		if reason != "" {
			fields["title"] = reason
		} else {
			p.Title = &title // store the normalised title, not the raw one
		}
	}
	if len(fields) > 0 {
		return Todo{}, &ValidationError{Fields: fields}
	}
	t, err := s.store.Update(ctx, id, p, s.Now())
	if err != nil {
		return Todo{}, fmt.Errorf("update todo %d: %w", id, err)
	}
	return t, nil
}

// Delete removes a todo; deleting one that does not exist is ErrNotFound, so
// a buggy client that deletes twice finds out.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete todo %d: %w", id, err)
	}
	return nil
}

func (s *Service) Stats(ctx context.Context) (Stats, error) {
	st, err := s.store.Stats(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("todo stats: %w", err)
	}
	return st, nil
}
