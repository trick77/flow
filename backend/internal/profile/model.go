package profile

import "time"

// Profile is the owner's profile and settings, the one row of its table.
//
// Table: owner_profile
type Profile struct {
	GitName         string
	GitEmail        string
	Language        string
	FormatLocale    string
	TimeZone        string
	SidebarGrouping string
	ConfigRepo      string
	ConfigSyncState string
	ConfigSyncedAt  *time.Time
	ConfigCommit    string
	ConfigFiles     int
	ConfigErrorCode string
	UpdatedAt       time.Time
}
