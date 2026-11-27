package profile

import (
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/projects"
)

// API implements httpapi.Profile: read and change the profile (the config sync answers not_implemented until
// flowd runs jobs).
type API struct {
	httpapi.NotImplemented
	Store Store
	// Changed is told after an update that changed the git identity: the credentials set flowd gets carries it, so
	// Flow pushes it to warm sandboxes. nil: nobody.
	Changed func()
	// Publish sends an app event (profile.updated); nil: none.
	Publish func(apitypes.AppEvent)
}

var _ httpapi.Profile = (*API)(nil)

// Bounds of the profile's text fields. Git name and email end up in a session unit's environment file, so a control
// character (a line break above all) is refused, never stored.
const (
	maxGitName  = 200
	maxGitEmail = 254
)

var (
	// emailRE is an address as git takes it: one @, no spaces or angle brackets (git's own delimiters).
	emailRE    = regexp.MustCompile(`^[^@\s<>]+@[^@\s<>]+$`)
	localeRE   = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$`)
	timeZoneRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)
)

// update is ProfileUpdate as sent: pointers, so an absent field stays and an empty one clears (the generated type's
// plain strings cannot tell the two apart).
type update struct {
	GitName         *string `json:"gitName"`
	GitEmail        *string `json:"gitEmail"`
	Language        *string `json:"language"`
	FormatLocale    *string `json:"formatLocale"`
	TimeZone        *string `json:"timeZone"`
	SidebarGrouping *string `json:"sidebarGrouping"`
	ConfigRepo      *string `json:"configRepo"`
}

func invalid(field string) error {
	return &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: field}
}

// text trims a field and checks its length and characters.
func text(v *string, field string, maxLen int) (*string, error) {
	if v == nil {
		return nil, nil
	}
	s := strings.TrimSpace(*v)
	if utf8.RuneCountInString(s) > maxLen || !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return nil, invalid(field)
	}
	return &s, nil
}

// matching is text whose value must match re (or be empty when emptyOK).
func matching(v *string, field string, maxLen int, re *regexp.Regexp, emptyOK bool) (*string, error) {
	s, err := text(v, field, maxLen)
	if err != nil || s == nil {
		return s, err
	}
	if *s == "" && emptyOK {
		return s, nil
	}
	if !re.MatchString(*s) {
		return nil, invalid(field)
	}
	return s, nil
}

// patch validates an update into a Patch.
func (u update) patch() (Patch, error) {
	var p Patch
	var err error
	if p.GitName, err = text(u.GitName, "gitName", maxGitName); err != nil {
		return p, err
	}
	if p.GitEmail, err = matching(u.GitEmail, "gitEmail", maxGitEmail, emailRE, true); err != nil {
		return p, err
	}
	if p.Language, err = matching(u.Language, "language", 35, localeRE, false); err != nil {
		return p, err
	}
	if p.FormatLocale, err = matching(u.FormatLocale, "formatLocale", 35, localeRE, false); err != nil {
		return p, err
	}
	if p.TimeZone, err = matching(u.TimeZone, "timeZone", 64, timeZoneRE, false); err != nil {
		return p, err
	}
	if u.SidebarGrouping != nil {
		g := apitypes.SidebarGrouping(*u.SidebarGrouping)
		if g != apitypes.SidebarGroupingStatus && g != apitypes.SidebarGroupingProject {
			return p, invalid("sidebarGrouping")
		}
		p.SidebarGrouping = u.SidebarGrouping
	}
	if u.ConfigRepo != nil {
		repo := strings.TrimSpace(*u.ConfigRepo)
		if repo != "" {
			full, ok := projects.NormalizeRepo(repo)
			if !ok {
				return p, invalid("configRepo")
			}
			repo = full
		}
		p.ConfigRepo = &repo
	}
	return p, nil
}

// View is the API's form of a profile.
func View(p Profile) apitypes.Profile {
	return apitypes.Profile{GitName: p.GitName, GitEmail: p.GitEmail, Language: p.Language,
		FormatLocale: p.FormatLocale, TimeZone: p.TimeZone, SidebarGrouping: apitypes.SidebarGrouping(p.SidebarGrouping),
		ConfigRepo: p.ConfigRepo, ConfigSync: apitypes.ConfigSync{State: apitypes.ConfigSyncState(p.ConfigSyncState),
			SyncedAt: p.ConfigSyncedAt, Commit: p.ConfigCommit, Files: p.ConfigFiles,
			ErrorCode: apitypes.ErrorCode(p.ConfigErrorCode)}}
}

// GetProfile implements httpapi.Profile.
func (a *API) GetProfile(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.Get(r.Context())
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, View(p))
}

// UpdateProfile implements httpapi.Profile: absent fields stay, empty text fields clear.
func (a *API) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	var in update
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	patch, err := in.patch()
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	before, err := a.Store.Get(r.Context())
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	after, err := a.Store.Update(r.Context(), patch)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if (before.GitName != after.GitName || before.GitEmail != after.GitEmail) && a.Changed != nil {
		a.Changed()
	}
	if a.Publish != nil {
		a.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindProfileUpdated})
	}
	httpapi.WriteJSON(w, http.StatusOK, View(after))
}
