// Package profile is the owner's profile and settings: git identity, formats, sidebar grouping and the agent
// config repository (owner: profile and settings area, docs/ownership.md).
package profile

import "context"

// Patch changes the fields that are set.
type Patch struct {
	GitName, GitEmail, Language, FormatLocale, TimeZone, SidebarGrouping, ConfigRepo *string
}

// ConfigSync is the result of a config repository sync.
type ConfigSync struct {
	State, Commit, ErrorCode string
	Files                    int
}

// Store reads and writes the profile row. The migration creates it; Get never finds it missing.
type Store interface {
	Get(ctx context.Context) (Profile, error)
	Update(ctx context.Context, p Patch) (Profile, error)
	SetConfigSync(ctx context.Context, s ConfigSync) error
}
