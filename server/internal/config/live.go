package config

import (
	"sync"
	"sync/atomic"
)

// LiveCatalog holds the current catalog. Readers get an immutable snapshot
// (never modify it); writers go through Update, which validates a copy,
// persists it and then swaps it in atomically.
type LiveCatalog struct {
	mu sync.Mutex // serializes writers
	p  atomic.Pointer[Catalog]
}

func NewLiveCatalog(c *Catalog) *LiveCatalog {
	l := &LiveCatalog{}
	l.p.Store(c)
	return l
}

// Get returns the current snapshot. Callers must treat it as read-only.
func (l *LiveCatalog) Get() *Catalog { return l.p.Load() }

// ValidationError is a user-facing rejection of a catalog change.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

// Invalid returns a ValidationError with msg.
func Invalid(msg string) error { return &ValidationError{msg} }

// Update applies fn to a copy of the catalog, validates it, persists it with
// save and publishes it. On any error the live catalog is unchanged.
// Validation failures are returned as *ValidationError.
func (l *LiveCatalog) Update(fn func(c *Catalog) error, save func(c *Catalog) error) (*Catalog, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := l.p.Load().Clone()
	if err := fn(next); err != nil {
		return nil, err
	}
	if err := next.Normalize(); err != nil {
		return nil, &ValidationError{err.Error()}
	}
	if err := next.CheckChangedURLs(l.p.Load()); err != nil {
		return nil, &ValidationError{err.Error()}
	}
	if err := save(next); err != nil {
		return nil, err
	}
	l.p.Store(next)
	return next, nil
}
