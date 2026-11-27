package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHub's endpoints (docs.github.com, "Generating a user access token for a GitHub App", "Refreshing user access
// tokens", REST "GitHub App installations").
const (
	WebURL = "https://github.com"
	APIURL = "https://api.github.com"
)

// deviceGrant is the device flow's grant type (RFC 8628).
const deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// maxBody bounds every answer read from GitHub.
const maxBody = 4 << 20

// Client talks to GitHub with plain net/http. Tokens travel only in request bodies and the Authorization header,
// never in a URL, and no error it returns carries one: errors name the endpoint path and the status.
type Client struct {
	ClientID string
	// Web and API are GitHub's base URLs (WebURL, APIURL); a test points them at a fake.
	Web, API string
	HTTP     *http.Client
}

// NewClient is a Client for github.com with bounded requests.
func NewClient(clientID string) *Client {
	return &Client{ClientID: clientID, Web: WebURL, API: APIURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// OAuthError is GitHub's OAuth error answer ({"error": "..."}), which it may send with status 200. Its Code is
// GitHub's (authorization_pending, slow_down, expired_token, access_denied, bad_refresh_token, ...); Interval is the
// new minimum after slow_down.
type OAuthError struct {
	Code     string
	Interval int
}

func (e *OAuthError) Error() string { return "github: oauth error " + e.Code }

// StatusError is a non-2xx answer without an OAuth error body.
type StatusError struct {
	Path   string
	Status int
}

func (e *StatusError) Error() string { return fmt.Sprintf("github: %s: status %d", e.Path, e.Status) }

// Transient reports whether a refresh failure may pass by itself (network, GitHub's 5xx, rate limits, an answer
// without an OAuth error): the stored authorization is kept and tried again. Only an OAuth error answer means
// GitHub refused the refresh token.
func Transient(err error) bool {
	var oe *OAuthError
	return !errors.As(err, &oe)
}

// DeviceCode is GitHub's answer to the device code request.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Token is GitHub's token answer. With expiring user tokens on, ExpiresIn is 28800 (8 hours) and
// RefreshTokenExpiresIn 15897600 (6 months); without, both are 0 and RefreshToken is empty.
type Token struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
}

// oauthError is the error part of an OAuth answer.
type oauthError struct {
	Error    string `json:"error"`
	Interval int    `json:"interval"`
}

// post sends an OAuth form (in the body, never the URL) to the web host and decodes the JSON answer into v; an
// "error" field is an *OAuthError, whatever the status.
func (c *Client) post(ctx context.Context, path string, form url.Values, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Web+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json") // the default answer is form-encoded
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s: %w", path, scrub(err))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("github: %s: %w", path, err)
	}
	var oe oauthError
	if json.Unmarshal(body, &oe) == nil && oe.Error != "" {
		return &OAuthError{Code: oe.Error, Interval: oe.Interval}
	}
	if resp.StatusCode/100 != 2 {
		return &StatusError{Path: path, Status: resp.StatusCode}
	}
	if json.Unmarshal(body, v) != nil {
		return fmt.Errorf("github: %s: unreadable answer", path)
	}
	return nil
}

// scrub drops the URL from a transport error (*url.Error prints it); Flow's URLs carry no token, but the habit
// keeps it so.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// RequestDeviceCode starts a device flow (POST /login/device/code with the client id; no scope: an App with
// expiring user tokens on gets a refresh token without one).
func (c *Client) RequestDeviceCode(ctx context.Context) (DeviceCode, error) {
	var a DeviceCode
	if err := c.post(ctx, "/login/device/code", url.Values{"client_id": {c.ClientID}}, &a); err != nil {
		return DeviceCode{}, err
	}
	if a.DeviceCode == "" || a.UserCode == "" {
		return DeviceCode{}, errors.New("github: /login/device/code: no code in the answer")
	}
	return a, nil
}

// PollDeviceToken asks once whether the owner confirmed the code. Pending and the other device-flow states come back
// as *OAuthError.
func (c *Client) PollDeviceToken(ctx context.Context, deviceCode string) (Token, error) {
	var a Token
	if err := c.post(ctx, "/login/oauth/access_token", url.Values{"client_id": {c.ClientID},
		"device_code": {deviceCode}, "grant_type": {deviceGrant}}, &a); err != nil {
		return Token{}, err
	}
	if a.AccessToken == "" {
		return Token{}, errors.New("github: /login/oauth/access_token: no token in the answer")
	}
	return a, nil
}

// Refresh trades the refresh token for a new pair. No client secret: GitHub requires it "unless the user access
// token was generated using the device flow". The used refresh token and the old access token stop working.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	var a Token
	if err := c.post(ctx, "/login/oauth/access_token", url.Values{"client_id": {c.ClientID},
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}, &a); err != nil {
		return Token{}, err
	}
	if a.AccessToken == "" {
		return Token{}, errors.New("github: refresh: no token in the answer")
	}
	return a, nil
}

// get reads one REST resource with the user token into v.
func (c *Client) get(ctx context.Context, token, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s: %w", path, scrub(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return &StatusError{Path: path, Status: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
		return fmt.Errorf("github: %s: unreadable answer", path)
	}
	return nil
}

// User is the account a token acts as (GET /user).
type User struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// User reads the token's account.
func (c *Client) User(ctx context.Context, token string) (User, error) {
	var u User
	if err := c.get(ctx, token, "/user", &u); err != nil {
		return User{}, err
	}
	if u.Login == "" {
		return User{}, errors.New("github: /user: no login")
	}
	return u, nil
}

// Repo is a repository as the REST API describes it.
type Repo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
}

// Repo reads one repository (GET /repos/{owner}/{name}).
func (c *Client) Repo(ctx context.Context, token, fullName string) (Repo, error) {
	var r Repo
	err := c.get(ctx, token, "/repos/"+fullName, &r)
	return r, err
}

// pageSize is GitHub's largest page for the installation listings.
const pageSize = 100

// Installation is one installation of the App the user can see.
type Installation struct {
	ID int64 `json:"id"`
}

// Installations lists the App's installations the user has explicit permission for (GET /user/installations), at
// most maxPages pages.
func (c *Client) Installations(ctx context.Context, token string, maxPages int) ([]Installation, error) {
	var out []Installation
	for page := 1; page <= maxPages; page++ {
		var a struct {
			TotalCount    int            `json:"total_count"`
			Installations []Installation `json:"installations"`
		}
		if err := c.get(ctx, token, fmt.Sprintf("/user/installations?per_page=%d&page=%d", pageSize, page), &a); err != nil {
			return nil, err
		}
		out = append(out, a.Installations...)
		if len(a.Installations) < pageSize || len(out) >= a.TotalCount {
			break
		}
	}
	return out, nil
}

// InstallationRepos lists the repositories of one installation the user can reach
// (GET /user/installations/{id}/repositories), at most limit.
func (c *Client) InstallationRepos(ctx context.Context, token string, id int64, limit int) ([]Repo, error) {
	var out []Repo
	for page := 1; len(out) < limit; page++ {
		var a struct {
			TotalCount   int    `json:"total_count"`
			Repositories []Repo `json:"repositories"`
		}
		path := fmt.Sprintf("/user/installations/%d/repositories?per_page=%d&page=%d", id, pageSize, page)
		if err := c.get(ctx, token, path, &a); err != nil {
			return nil, err
		}
		out = append(out, a.Repositories...)
		if len(a.Repositories) < pageSize || len(out) >= a.TotalCount {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
