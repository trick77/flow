// Package projects owns projects and their repositories (owner: projects area, docs/ownership.md).
package projects

import (
	"context"
	"errors"
)

// ErrNotFound means no such project or repository.
var ErrNotFound = errors.New("projects: not found")

// Store keeps projects and their repositories.
type Store interface {
	List(ctx context.Context) ([]Project, error)
	Get(ctx context.Context, id string) (Project, error)
	Create(ctx context.Context, p Project, repos []Repo) error
	Rename(ctx context.Context, id, name string) error
	Delete(ctx context.Context, id string) error
	Repos(ctx context.Context, projectID string) ([]Repo, error)
	Repo(ctx context.Context, id string) (Repo, error)
	AddRepo(ctx context.Context, r Repo) error
	// RemoveRepo fails while a session worktree refers to the repository (the foreign key restricts it).
	RemoveRepo(ctx context.Context, id string) error
}
