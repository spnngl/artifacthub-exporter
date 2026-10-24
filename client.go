// Package artifacthub is a minimal client of the Artifact Hub API
// (https://artifacthub.io/docs/api/), limited to what the exporter reads.
package artifacthub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	// searchLimit is the largest page the API serves.
	searchLimit = 60
	// maxRetries is how many times a request that failed for a transient reason is sent again.
	maxRetries = 3
	// maxRetryAfter caps the wait before a retry, whether a Retry-After header or
	// the backoff sets it.
	maxRetryAfter = time.Minute
	// rateLimitFactor lengthens the backoff of a 429 without Retry-After: the rate
	// limit in front of the API blocks a client for about a minute, which 2, 4
	// and 8 seconds don't outwait.
	rateLimitFactor = 15
	// maxBodySize bounds the response bodies read.
	maxBodySize = 32 << 20
)

// Endpoints, as route templates: the label of the request counter.
const (
	EndpointPackageSearch    = "/packages/search"
	EndpointPackage          = "/packages/{kind}/{repository}/{package}"
	EndpointPackageViews     = "/packages/{id}/views"
	EndpointRepositorySearch = "/repositories/search"
	EndpointPackageStats     = "/packages/stats"
)

// ErrNotFound is the error of a 404.
var ErrNotFound = errors.New("not found")

// ErrUnknownKind is the error of a package request for a kind the client doesn't know.
var ErrUnknownKind = errors.New("unknown kind")

// StatusError is the error of a response with an unexpected status.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected status %d", e.Code)
}

// Is makes a 404 match ErrNotFound.
func (e *StatusError) Is(target error) bool {
	return target == ErrNotFound && e.Code == http.StatusNotFound
}

// retryableError marks an error worth another try, and how long to wait for it
// when the server said so.
type retryableError struct {
	err         error
	after       time.Duration
	rateLimited bool
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

// Config configures a Client.
type Config struct {
	// BaseURL is the hub's address, e.g. https://artifacthub.io.
	BaseURL string
	// APIKeyID and APIKeySecret are optional, both or neither.
	APIKeyID     string
	APIKeySecret string
	// UserAgent is sent with every request.
	UserAgent string
	// HTTPClient defaults to one with a 30 s timeout.
	HTTPClient *http.Client
	// RequestInterval is the least time between two requests, retries included.
	// Zero sends them as fast as they come.
	RequestInterval time.Duration
	// RetryDelay is the first wait before a retry, doubled each time. Defaults to 2 s.
	RetryDelay time.Duration
	// OnRequest, when set, is called with the endpoint and the status code
	// (or `error`) of every attempt.
	OnRequest func(endpoint, code string)
}

// Client reads the Artifact Hub API. Its methods are safe for concurrent use.
type Client struct {
	// mu guards next, the earliest time of the next request.
	mu           sync.Mutex
	next         time.Time
	interval     time.Duration
	apiURL       *url.URL
	apiKeyID     string
	apiKeySecret string
	userAgent    string
	http         *http.Client
	retryDelay   time.Duration
	onRequest    func(endpoint, code string)
}

// New returns a Client for the given configuration.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("base URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("base URL %q: want an absolute http or https URL", cfg.BaseURL)
	}
	if (cfg.APIKeyID == "") != (cfg.APIKeySecret == "") {
		return nil, errors.New("set both the API key id and secret, or neither")
	}
	c := &Client{
		apiURL:       u.JoinPath("api", "v1"),
		apiKeyID:     cfg.APIKeyID,
		apiKeySecret: cfg.APIKeySecret,
		userAgent:    cfg.UserAgent,
		http:         cfg.HTTPClient,
		retryDelay:   cfg.RetryDelay,
		interval:     cfg.RequestInterval,
		onRequest:    cfg.OnRequest,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.retryDelay == 0 {
		c.retryDelay = 2 * time.Second
	}
	return c, nil
}

// PackageQuery selects the packages of one organization, user or repository.
// Set one field.
type PackageQuery struct {
	Org        string
	User       string
	Repository string
}

// RepositoryQuery selects the repositories of one organization or user, or
// the one named Name. Set one field.
type RepositoryQuery struct {
	Org  string
	User string
	Name string
}

// packageSearchBody is the JSON shape of a package search response.
type packageSearchBody struct {
	Packages []PackageSummary `json:"packages"`
}

// SearchPackages returns every package matching the query, deprecated ones included.
func (c *Client) SearchPackages(ctx context.Context, q PackageQuery) ([]PackageSummary, error) {
	params := url.Values{"deprecated": {"true"}, "facets": {"false"}}
	setIfNotEmpty(params, "org", q.Org)
	setIfNotEmpty(params, "user", q.User)
	setIfNotEmpty(params, "repo", q.Repository)
	return paginate(ctx, c, EndpointPackageSearch, []string{"packages", "search"}, params,
		func(body packageSearchBody) []PackageSummary { return body.Packages },
		func(p PackageSummary) string { return p.ID })
}

// SearchRepositories returns every repository matching the query. A Name
// matches exactly: the API takes it as a pattern.
func (c *Client) SearchRepositories(ctx context.Context, q RepositoryQuery) ([]Repository, error) {
	params := url.Values{}
	setIfNotEmpty(params, "org", q.Org)
	setIfNotEmpty(params, "user", q.User)
	setIfNotEmpty(params, "name", q.Name)
	repos, err := paginate(ctx, c, EndpointRepositorySearch, []string{"repositories", "search"}, params,
		func(body []Repository) []Repository { return body },
		func(r Repository) string { return r.ID })
	if err != nil {
		return nil, err
	}
	if q.Name == "" {
		return repos, nil
	}
	exact := repos[:0]
	for _, r := range repos {
		if r.Name == q.Name {
			exact = append(exact, r)
		}
	}
	return exact, nil
}

// GetPackageDetails returns the details of a package.
func (c *Client) GetPackageDetails(ctx context.Context, kind Kind, repository, name string) (PackageDetails, error) {
	slug, ok := kind.Slug()
	if !ok {
		return PackageDetails{}, fmt.Errorf("package %s/%s: %w %d", repository, name, ErrUnknownKind, int(kind))
	}
	var d PackageDetails
	_, err := c.get(ctx, EndpointPackage, &d, url.Values{}, "packages", slug, repository, name)
	return d, err
}

// GetViews returns the views of a package in the last month.
func (c *Client) GetViews(ctx context.Context, packageID string) (Views, error) {
	var v Views
	_, err := c.get(ctx, EndpointPackageViews, &v, url.Values{}, "packages", packageID, "views")
	return v, err
}

// GetHubStats returns what the hub holds in total.
func (c *Client) GetHubStats(ctx context.Context) (HubStats, error) {
	var s HubStats
	_, err := c.get(ctx, EndpointPackageStats, &s, url.Values{}, "packages", "stats")
	return s, err
}

func setIfNotEmpty(params url.Values, key, value string) {
	if value != "" {
		params.Set(key, value)
	}
}

// paginate fetches the pages of a search until the results end, and returns
// the items once each: the API's order isn't stable between pages.
// Body is the response's JSON shape, items extracts the page from it.
func paginate[Body any, Item any](
	ctx context.Context, c *Client, endpoint string, segments []string, params url.Values,
	items func(Body) []Item, id func(Item) string,
) ([]Item, error) {
	var all []Item
	seen := make(map[string]struct{})
	params.Set("limit", strconv.Itoa(searchLimit))
	for offset := 0; ; {
		params.Set("offset", strconv.Itoa(offset))
		var body Body
		header, err := c.get(ctx, endpoint, &body, params, segments...)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, item := range items(body) {
			if _, dup := seen[id(item)]; dup {
				continue
			}
			seen[id(item)] = struct{}{}
			all = append(all, item)
			added++
		}
		offset += searchLimit
		// A page with nothing new is the end, or a server ignoring the offset.
		if added == 0 {
			return all, nil
		}
		if total, err := strconv.Atoi(header.Get("Pagination-Total-Count")); err == nil && offset >= total {
			return all, nil
		}
	}
}

// get sends a GET to the API path made of the given segments, retries
// transient failures, and decodes the JSON response into out.
func (c *Client) get(
	ctx context.Context, endpoint string, out any, params url.Values, segments ...string,
) (http.Header, error) {
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	u := c.apiURL.JoinPath(escaped...)
	u.RawQuery = params.Encode()

	for attempt := 0; ; attempt++ {
		header, err := c.attempt(ctx, endpoint, u, out)
		var retry *retryableError
		if err == nil || !errors.As(err, &retry) || attempt >= maxRetries {
			return header, err
		}
		delay := retry.after
		if delay == 0 {
			base := c.retryDelay
			if retry.rateLimited {
				base *= rateLimitFactor
			}
			delay = min(base<<attempt, maxRetryAfter)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (c *Client) attempt(ctx context.Context, endpoint string, u *url.URL, out any) (http.Header, error) {
	if err := c.pace(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.apiKeyID != "" {
		req.Header.Set("X-API-KEY-ID", c.apiKeyID)
		req.Header.Set("X-API-KEY-SECRET", c.apiKeySecret)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.record(endpoint, "error")
		err = fmt.Errorf("GET %s: %w", u.Path, err)
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, &retryableError{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	c.record(endpoint, strconv.Itoa(resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("GET %s: %w", u.Path, &StatusError{Code: resp.StatusCode})
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, &retryableError{
				err: err, after: retryAfter(resp.Header),
				rateLimited: resp.StatusCode == http.StatusTooManyRequests,
			}
		}
		return nil, err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodySize)).Decode(out); err != nil {
		return nil, fmt.Errorf("GET %s: decode response: %w", u.Path, err)
	}
	return resp.Header, nil
}

// pace waits for the turn of the request: the API answers a burst with 429s.
func (c *Client) pace(ctx context.Context) error {
	if c.interval <= 0 {
		return nil
	}
	c.mu.Lock()
	now := time.Now()
	at := now
	if c.next.After(now) {
		at = c.next
	}
	c.next = at.Add(c.interval)
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(at.Sub(now)):
		return nil
	}
}

func (c *Client) record(endpoint, code string) {
	if c.onRequest != nil {
		c.onRequest(endpoint, code)
	}
}

// retryAfter returns the wait a Retry-After header (in seconds) asks for, or 0.
func retryAfter(h http.Header) time.Duration {
	secs, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil || secs <= 0 {
		return 0
	}
	return min(time.Duration(secs)*time.Second, maxRetryAfter)
}
