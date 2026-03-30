package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Client struct {
	ClientID  string
	Tenant    string
	Scopes    []string
	HTTP      *http.Client
	UserAgent string
	Verbose   bool
}

type DeviceCodeResponse struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
	Message         string `json:"message"`
}

type TokenResponse struct {
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	ExtExpiresIn int64  `json:"ext_expires_in"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type ErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorCodes       []int  `json:"error_codes"`
	Timestamp        string `json:"timestamp"`
	TraceID          string `json:"trace_id"`
	CorrelationID    string `json:"correlation_id"`
}

func (c *Client) DeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	if c.ClientID == "" {
		return nil, errors.New("client_id is required")
	}
	tenant := c.Tenant
	if tenant == "" {
		tenant = "common"
	}

	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("scope", strings.Join(c.Scopes, " "))

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/devicecode", url.PathEscape(tenant))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Verbose {
		fmt.Fprintln(os.Stderr, "POST", u)
	}

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if c.Verbose {
		fmt.Fprintln(os.Stderr, "Status", res.Status)
	}

	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var er ErrorResponse
		_ = json.Unmarshal(b, &er)
		if er.Error != "" {
			return nil, fmt.Errorf("device code failed: %s: %s", er.Error, er.ErrorDescription)
		}
		return nil, fmt.Errorf("device code failed: %s", strings.TrimSpace(string(b)))
	}

	var dc DeviceCodeResponse
	if err := json.Unmarshal(b, &dc); err != nil {
		return nil, err
	}
	return &dc, nil
}

func (c *Client) PollToken(ctx context.Context, dc *DeviceCodeResponse) (*TokenResponse, error) {
	if dc == nil || dc.DeviceCode == "" {
		return nil, errors.New("device code is required")
	}
	tenant := c.Tenant
	if tenant == "" {
		tenant = "common"
	}

	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(tenant))
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}

	for {
		if time.Now().After(deadline) {
			return nil, errors.New("device code expired")
		}

		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("client_id", c.ClientID)
		form.Set("device_code", dc.DeviceCode)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		if c.Verbose {
			fmt.Fprintln(os.Stderr, "POST", u)
		}

		res, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		if c.Verbose {
			fmt.Fprintln(os.Stderr, "Status", res.Status)
		}

		if res.StatusCode >= 200 && res.StatusCode < 300 {
			var tr TokenResponse
			if err := json.Unmarshal(b, &tr); err != nil {
				return nil, err
			}
			if tr.AccessToken == "" {
				return nil, errors.New("token response missing access_token")
			}
			return &tr, nil
		}

		var er ErrorResponse
		_ = json.Unmarshal(b, &er)
		switch er.Error {
		case "authorization_pending":
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(interval):
				continue
			}
		case "slow_down":
			interval += 5 * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(interval):
				continue
			}
		default:
			if er.Error != "" {
				return nil, fmt.Errorf("token polling failed: %s: %s", er.Error, er.ErrorDescription)
			}
			return nil, fmt.Errorf("token polling failed: %s", strings.TrimSpace(string(b)))
		}
	}
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	if c.ClientID == "" {
		return nil, errors.New("client_id is required")
	}
	if refreshToken == "" {
		return nil, errors.New("refresh_token is required")
	}
	tenant := c.Tenant
	if tenant == "" {
		tenant = "common"
	}

	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if len(c.Scopes) > 0 {
		form.Set("scope", strings.Join(c.Scopes, " "))
	}

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(tenant))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Verbose {
		fmt.Fprintln(os.Stderr, "POST", u)
	}

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if c.Verbose {
		fmt.Fprintln(os.Stderr, "Status", res.Status)
	}

	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var er ErrorResponse
		_ = json.Unmarshal(b, &er)
		if er.Error != "" {
			return nil, fmt.Errorf("refresh failed: %s: %s", er.Error, er.ErrorDescription)
		}
		return nil, fmt.Errorf("refresh failed: %s", strings.TrimSpace(string(b)))
	}

	var tr TokenResponse
	if err := json.Unmarshal(b, &tr); err != nil {
		return nil, err
	}
	if tr.AccessToken == "" {
		return nil, errors.New("refresh response missing access_token")
	}
	return &tr, nil
}
