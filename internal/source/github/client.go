// Package github implements the GitHub HTTP adapter used by source.Manager.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thewelshrich/schooner/internal/source"
)

const (
	maxResponseBytes = 1 << 20
	apiVersion       = "2026-03-10"
)

type Options struct {
	ClientID  string
	HTTP      *http.Client
	APIBase   string
	LoginBase string
	Presenter source.DevicePresenter
}

type Client struct {
	clientID  string
	http      *http.Client
	apiBase   string
	loginBase string
	presenter source.DevicePresenter
	now       func() time.Time
	wait      func(context.Context, time.Duration) error
}

func New(options Options) *Client {
	if options.HTTP == nil {
		options.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if options.APIBase == "" {
		options.APIBase = "https://api.github.com"
	}
	if options.LoginBase == "" {
		options.LoginBase = "https://github.com"
	}
	return &Client{
		clientID: strings.TrimSpace(options.ClientID), http: options.HTTP,
		apiBase: strings.TrimRight(options.APIBase, "/"), loginBase: strings.TrimRight(options.LoginBase, "/"),
		presenter: options.Presenter, now: time.Now, wait: waitContext,
	}
}

func (c *Client) Authorize(ctx context.Context) (source.Token, error) {
	if c.clientID == "" {
		return source.Token{}, &source.Error{Code: "authentication_required", Message: "this Schooner build has no GitHub device-flow client ID", Context: map[string]string{"reason": "credentials_missing"}}
	}
	if c.presenter == nil {
		return source.Token{}, &source.Error{Code: "authentication_required", Message: "interactive GitHub authorization is unavailable", Context: map[string]string{"reason": "credentials_missing"}}
	}
	form := url.Values{"client_id": {c.clientID}}
	var device struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := c.oauth(ctx, "/login/device/code", form, &device); err != nil {
		return source.Token{}, err
	}
	if !validDeviceAuthorization(device.DeviceCode, device.UserCode, device.VerificationURI, device.ExpiresIn, device.Interval) {
		return source.Token{}, unavailable("GitHub returned an invalid device authorization", nil)
	}
	expiresAt := c.now().UTC().Add(time.Duration(device.ExpiresIn) * time.Second)
	if err := c.presenter.Present(ctx, source.DeviceAuthorization{VerificationURI: device.VerificationURI, UserCode: device.UserCode, ExpiresAt: expiresAt}); err != nil {
		return source.Token{}, err
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	for c.now().UTC().Before(expiresAt) {
		if err := c.wait(ctx, interval); err != nil {
			return source.Token{}, err
		}
		if !c.now().UTC().Before(expiresAt) {
			return source.Token{}, source.NewError("authentication_required", "GitHub device authorization expired", nil)
		}
		form = url.Values{
			"client_id":   {c.clientID},
			"device_code": {device.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		var response tokenResponse
		err := c.oauth(ctx, "/login/oauth/access_token", form, &response)
		if err == nil && response.Error == "" {
			return response.token(c.now().UTC())
		}
		switch response.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied":
			return source.Token{}, source.NewError("authentication_required", "GitHub authorization was denied", nil)
		case "expired_token":
			return source.Token{}, source.NewError("authentication_required", "GitHub device authorization expired", nil)
		case "":
			return source.Token{}, err
		default:
			return source.Token{}, source.NewError("authentication_required", "GitHub device authorization failed", nil)
		}
	}
	return source.Token{}, source.NewError("authentication_required", "GitHub device authorization expired", nil)
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (source.Token, error) {
	if !validToken(refreshToken) || c.clientID == "" {
		return source.Token{}, source.NewError("authentication_required", "GitHub authorization cannot be refreshed", nil)
	}
	form := url.Values{
		"client_id":     {c.clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	var response tokenResponse
	if err := c.oauth(ctx, "/login/oauth/access_token", form, &response); err != nil {
		return source.Token{}, err
	}
	if response.Error != "" {
		return source.Token{}, source.NewError("authentication_required", "GitHub authorization needs to be renewed", nil)
	}
	return response.token(c.now().UTC())
}

type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
}

func (r tokenResponse) token(now time.Time) (source.Token, error) {
	if !validToken(r.AccessToken) || !validToken(r.RefreshToken) || r.ExpiresIn <= 0 || r.ExpiresIn > 7*24*60*60 || r.RefreshTokenExpiresIn <= 0 || r.RefreshTokenExpiresIn > 2*366*24*60*60 {
		return source.Token{}, source.NewError("authentication_required", "GitHub returned an incomplete authorization", nil)
	}
	result := source.Token{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken}
	if r.ExpiresIn > 0 {
		result.AccessExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	if r.RefreshTokenExpiresIn > 0 {
		result.RefreshExpiresAt = now.Add(time.Duration(r.RefreshTokenExpiresIn) * time.Second)
	}
	return result, nil
}

func (c *Client) Account(ctx context.Context, accessToken string) (source.RemoteAccount, error) {
	var response struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := c.api(ctx, http.MethodGet, "/user", accessToken, nil, &response, http.StatusOK); err != nil {
		return source.RemoteAccount{}, err
	}
	if response.ID <= 0 || !validLogin(response.Login) {
		return source.RemoteAccount{}, unavailable("GitHub returned invalid account metadata", nil)
	}
	return source.RemoteAccount{ID: strconv.FormatInt(response.ID, 10), Login: response.Login}, nil
}

func (c *Client) HostKeys(ctx context.Context) ([]source.HostKey, error) {
	var response struct {
		SSHKeys         []string          `json:"ssh_keys"`
		SSHFingerprints map[string]string `json:"ssh_key_fingerprints"`
	}
	if err := c.api(ctx, http.MethodGet, "/meta", "", nil, &response, http.StatusOK); err != nil {
		return nil, err
	}
	advertised := map[string]bool{}
	for _, fingerprint := range response.SSHFingerprints {
		advertised[fingerprint] = true
	}
	keys := make([]source.HostKey, 0, len(response.SSHKeys))
	for _, key := range response.SSHKeys {
		fingerprint, err := source.PublicKeyFingerprint(key)
		if err != nil || !advertised[fingerprint] {
			return nil, &source.Error{Code: "conflict", Message: "GitHub host-key metadata failed fingerprint validation", Context: map[string]string{"reason": "host_key_changed"}, Cause: err}
		}
		keys = append(keys, source.HostKey{Key: key, Fingerprint: fingerprint})
	}
	keys = source.SortedHostKeys(keys)
	if err := source.ValidateHostKeys(keys); err != nil {
		return nil, &source.Error{Code: "conflict", Message: "GitHub host-key metadata is invalid", Context: map[string]string{"reason": "host_key_changed"}, Cause: err}
	}
	return keys, nil
}

func (c *Client) ListKeys(ctx context.Context, accessToken string) ([]source.RemoteKey, error) {
	result := []source.RemoteKey{}
	for page := 1; page <= 10; page++ {
		var response []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Key   string `json:"key"`
		}
		headers, err := c.apiHeaders(ctx, http.MethodGet, fmt.Sprintf("/user/keys?per_page=100&page=%d", page), accessToken, nil, &response, http.StatusOK)
		if err != nil {
			return nil, err
		}
		for _, key := range response {
			fingerprint, fingerprintErr := source.PublicKeyFingerprint(key.Key)
			if fingerprintErr != nil || key.ID <= 0 || !validTitle(key.Title) {
				return nil, unavailable("GitHub returned invalid SSH key metadata", fingerprintErr)
			}
			result = append(result, source.RemoteKey{ID: key.ID, Title: key.Title, PublicKey: key.Key, Fingerprint: fingerprint})
		}
		if len(response) < 100 || !strings.Contains(headers.Get("Link"), `rel="next"`) {
			return result, nil
		}
	}
	return nil, unavailable("GitHub SSH key catalog exceeds Schooner's safety limit", nil)
}

func (c *Client) CreateKey(ctx context.Context, accessToken, title, publicKey string) (source.RemoteKey, error) {
	if !validTitle(title) {
		return source.RemoteKey{}, source.NewError("invalid_input", "GitHub SSH key title is invalid", nil)
	}
	if _, err := source.PublicKeyFingerprint(publicKey); err != nil {
		return source.RemoteKey{}, source.NewError("invalid_input", "GitHub SSH public key is invalid", err)
	}
	payload := struct {
		Title string `json:"title"`
		Key   string `json:"key"`
	}{Title: title, Key: publicKey}
	var response struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		Key   string `json:"key"`
	}
	if _, err := c.apiHeaders(ctx, http.MethodPost, "/user/keys", accessToken, payload, &response, http.StatusCreated); err != nil {
		return source.RemoteKey{}, err
	}
	fingerprint, err := source.PublicKeyFingerprint(response.Key)
	if err != nil || response.ID <= 0 || !validTitle(response.Title) {
		return source.RemoteKey{}, unavailable("GitHub returned invalid SSH key metadata", err)
	}
	return source.RemoteKey{ID: response.ID, Title: response.Title, PublicKey: response.Key, Fingerprint: fingerprint}, nil
}

func (c *Client) DeleteKey(ctx context.Context, accessToken string, id int64) (bool, error) {
	if id <= 0 {
		return false, source.NewError("invalid_input", "GitHub SSH key ID is invalid", nil)
	}
	request, err := c.request(ctx, http.MethodDelete, fmt.Sprintf("%s/user/keys/%d", c.apiBase, id), accessToken, nil)
	if err != nil {
		return false, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return false, unavailable("GitHub SSH key revocation is unavailable", err)
	}
	defer response.Body.Close()
	read, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil || read > maxResponseBytes {
		return false, unavailable("GitHub SSH key revocation returned an invalid response", readErr)
	}
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusNoContent {
		return false, statusError(response, "GitHub SSH key revocation failed")
	}
	return true, nil
}

func (c *Client) oauth(ctx context.Context, path string, form url.Values, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.loginBase+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.http.Do(request)
	if err != nil {
		return unavailable("GitHub authorization is unavailable", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return statusError(response, "GitHub authorization failed")
	}
	return decodeBounded(response.Body, target)
}

func (c *Client) api(ctx context.Context, method, path, token string, body, target any, expected int) error {
	_, err := c.apiHeaders(ctx, method, path, token, body, target, expected)
	return err
}

func (c *Client) apiHeaders(ctx context.Context, method, path, token string, body, target any, expected int) (http.Header, error) {
	request, err := c.request(ctx, method, c.apiBase+path, token, body)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, unavailable("GitHub is unavailable", err)
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		return response.Header, statusError(response, "GitHub request failed")
	}
	if target != nil {
		if err = decodeBounded(response.Body, target); err != nil {
			return response.Header, err
		}
	}
	return response.Header, nil
}

func (c *Client) request(ctx context.Context, method, endpoint, token string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	request.Header.Set("User-Agent", "schooner-cli")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request, nil
}

func statusError(response *http.Response, message string) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return source.NewError("authentication_required", "GitHub authorization needs to be renewed", nil)
	case http.StatusForbidden:
		if response.Header.Get("X-RateLimit-Remaining") == "0" {
			return unavailable("GitHub rate limiting prevents source inspection", nil)
		}
		return source.NewError("permission_denied", "the GitHub App lacks permission to manage Git SSH keys", nil)
	case http.StatusConflict, http.StatusUnprocessableEntity:
		return source.NewError("conflict", message, nil)
	default:
		if response.StatusCode >= 500 {
			return unavailable(message, nil)
		}
		return unavailable(message, fmt.Errorf("HTTP status %d", response.StatusCode))
	}
}

func decodeBounded(reader io.Reader, target any) error {
	contents, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return unavailable("GitHub response could not be read", err)
	}
	if len(contents) == 0 || len(contents) > maxResponseBytes {
		return unavailable("GitHub response is empty or exceeds 1 MiB", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err = decoder.Decode(target); err != nil {
		return unavailable("GitHub returned an invalid response", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return unavailable("GitHub returned an invalid response", nil)
	}
	return nil
}

func validTitle(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}

func validDeviceAuthorization(deviceCode, userCode, verificationURI string, expiresIn, interval int) bool {
	if deviceCode == "" || len(deviceCode) > 1024 || strings.ContainsAny(deviceCode, "\x00\r\n") || userCode == "" || len(userCode) > 32 {
		return false
	}
	for _, character := range userCode {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	parsed, err := url.Parse(verificationURI)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "github.com" && parsed.User == nil && expiresIn > 0 && expiresIn <= 3600 && interval >= 0 && interval <= 60
}

func validToken(value string) bool {
	return value != "" && len(value) <= 16<<10 && !strings.ContainsAny(value, "\x00\r\n")
}

func validLogin(value string) bool {
	if value == "" || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func unavailable(message string, cause error) error {
	return source.NewError(source.CodeSourceUnavailable, message, cause)
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
