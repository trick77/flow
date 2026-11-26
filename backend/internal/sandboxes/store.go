// Package sandboxes is Flow's side of the sandbox lifecycle: one sandbox per project, created from the base image
// through sandbox.Runtime, flowd delivered and started, setup scripts run, idle sandboxes stopped under the RAM
// budget (owner: sandboxes area, docs/ownership.md).
package sandboxes

import (
	"context"
	"errors"
)

// ErrNotFound means the project has no sandbox row yet.
var ErrNotFound = errors.New("sandboxes: not found")

// States of a sandbox, as the API reports them (apitypes.SandboxState).
const (
	StateNone     = "none"
	StateSetup    = "setup"
	StateWarm     = "warm"
	StateStopping = "stopping"
	StateStopped  = "stopped"
	StateFailed   = "failed"
	StateDeleting = "deleting"
)

// Store keeps sandbox rows.
type Store interface {
	ByProject(ctx context.Context, projectID string) (Sandbox, error)
	List(ctx context.Context) ([]Sandbox, error)
	Put(ctx context.Context, s Sandbox) error
	SetState(ctx context.Context, id, state, progressCode, errorCode string) error
	Touch(ctx context.Context, id string) error
}
