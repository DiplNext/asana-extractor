// Package asana is a minimal read-only client for the Asana REST API that
// lists workspace users and projects with pagination, request pacing and
// bounded retries.
package asana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	pageSize       = 100
	maxBodyBytes   = 16 << 20 // a 100-item page is far smaller; guards against runaway bodies
	maxErrorBytes  = 64 << 10
	backoffBase    = time.Second
	backoffCap     = 30 * time.Second
	userAgent      = "asana-extractor/1.0"
	defaultTimeout = 30 * time.Second
)

type Options struct {
	BaseURL            string
	Token              string
	HTTPTimeout        time.Duration
	RateLimitPerMinute int
	MaxRetries         int
	Logger             *slog.Logger
}

type Client struct {
	baseURL    string
	token      string
	http       *http.Client
	maxRetries int
	log        *slog.Logger
	pacer      *pacer
	retries    atomic.Int64

	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration // uniform in [0, d]
}

func New(opts Options) *Client {
	timeout := opts.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	rate := max(opts.RateLimitPerMinute, 1)
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout

	c := &Client{
		baseURL: opts.BaseURL,
		token:   opts.Token,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			// The API needs no redirects; not following them means the
			// Authorization header can never be forwarded elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxRetries: max(opts.MaxRetries, 0),
		log:        log,
		now:        time.Now,
		sleep:      sleepCtx,
		jitter:     func(d time.Duration) time.Duration { return rand.N(d + 1) },
	}
	c.pacer = newPacer(rate, c.nowFn, c.sleepFn)
	return c
}

// Indirections so tests can swap now/sleep after New.
func (c *Client) nowFn() time.Time                                   { return c.now() }
func (c *Client) sleepFn(ctx context.Context, d time.Duration) error { return c.sleep(ctx, d) }

// Retries returns the total number of retried requests since creation.
func (c *Client) Retries() int64 { return c.retries.Load() }

// ListUsers pages through users of the workspace visible to the token's
// user, calling fn once per page. Returning an error from fn stops paging.
func (c *Client) ListUsers(ctx context.Context, workspaceGID string, fn func([]User) error) error {
	q := url.Values{"workspace": {workspaceGID}, "opt_fields": {userFields}}
	return list(ctx, c, "list users", "/users", q, fn)
}

// ListProjects pages through all projects (archived and not archived) of
// the workspace visible to the token's user, calling fn once per page.
func (c *Client) ListProjects(ctx context.Context, workspaceGID string, fn func([]Project) error) error {
	q := url.Values{"workspace": {workspaceGID}, "opt_fields": {projectFields}}
	return list(ctx, c, "list projects", "/projects", q, fn)
}

func list[T any](ctx context.Context, c *Client, op, path string, base url.Values, fn func([]T) error) error {
	q := url.Values{}
	for k, v := range base {
		q[k] = append([]string(nil), v...)
	}
	q.Set("limit", strconv.Itoa(pageSize))

	prevOffset := ""
	for n := 1; ; n++ {
		pageOp := fmt.Sprintf("%s page %d", op, n)
		var p page[T]
		if err := c.get(ctx, pageOp, path, q, &p); err != nil {
			return err
		}
		if err := fn(p.Data); err != nil {
			return fmt.Errorf("%s: %w", pageOp, err)
		}
		if p.NextPage == nil {
			return nil
		}
		offset := p.NextPage.Offset
		if offset == "" || offset == prevOffset {
			return fmt.Errorf("%s: invalid next_page offset", pageOp)
		}
		prevOffset = offset
		q.Set("offset", offset)
	}
}

const maxRetryAfterSecs = math.MaxInt64 / int64(time.Second)

var errBodyTooLarge = errors.New("response body too large")

type response struct {
	status int
	header http.Header
	body   []byte
}

// get performs a paced GET with retries and decodes a 200 body into out.
func (c *Client) get(ctx context.Context, op, path string, q url.Values, out any) error {
	target := c.baseURL + path + "?" + q.Encode()
	for attempt := 0; ; attempt++ {
		if err := c.pacer.wait(ctx); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}

		resp, err := c.do(ctx, target)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", op, ctxErr)
		}
		if err == nil && resp.status == http.StatusOK {
			if err := json.Unmarshal(resp.body, out); err != nil {
				return fmt.Errorf("%s: decode response: %w", op, err)
			}
			return nil
		}

		var failure error
		retryable := true
		if err != nil {
			failure = fmt.Errorf("%s: %w", op, err)
			retryable = !errors.Is(err, errBodyTooLarge)
		} else {
			failure = newAPIError(op, resp.status, resp.body)
			retryable = retryableStatus(resp.status)
		}
		if !retryable {
			return failure
		}

		delay, serverDelay := c.retryDelay(resp, attempt)
		is429 := resp != nil && resp.status == http.StatusTooManyRequests
		if is429 {
			// Shared cooldown, set even when giving up: no later request made
			// with this token (next page, next resource, next run) may start
			// before the server allows it.
			c.pacer.cooldown(c.now().Add(delay))
		}
		if attempt >= c.maxRetries {
			return fmt.Errorf("giving up after %d attempts: %w", attempt+1, failure)
		}

		c.retries.Add(1)
		c.log.Warn("retrying asana request",
			"op", op, "attempt", attempt+1, "delay", delay, "server_delay", serverDelay, "err", failure)
		if is429 {
			continue // the next pacer.wait blocks until the cooldown ends
		}
		if err := c.sleep(ctx, delay); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
	}
}

// retryDelay returns how long to wait before the next attempt. A valid
// Retry-After on a 429 is used as-is (never capped or shortened); otherwise
// capped exponential backoff with equal jitter is used.
func (c *Client) retryDelay(resp *response, attempt int) (delay time.Duration, serverDelay bool) {
	if resp != nil && resp.status == http.StatusTooManyRequests {
		if d, ok := parseRetryAfter(resp.header.Get("Retry-After"), c.now()); ok {
			return d, true
		}
	}
	d := backoffCap
	if attempt < 10 {
		d = min(backoffBase<<attempt, backoffCap)
	}
	return d/2 + c.jitter(d/2), false
}

// parseRetryAfter accepts delta-seconds or an HTTP-date (RFC 9110).
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		if int64(secs) > maxRetryAfterSecs {
			return time.Duration(math.MaxInt64), true // saturate instead of overflowing to a negative wait
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// do sends one GET and fully reads the body so the connection can be reused.
func (c *Client) do(ctx context.Context, target string) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, redactURLError(err)
	}
	defer resp.Body.Close()

	limit := int64(maxErrorBytes)
	if resp.StatusCode == http.StatusOK {
		limit = maxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(body)) > limit {
		if resp.StatusCode == http.StatusOK {
			return nil, fmt.Errorf("%w (%d bytes)", errBodyTooLarge, limit)
		}
		body = body[:limit]
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return &response{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// redactURLError drops the query string (workspace filters, offsets) from
// transport errors to keep logs short; the token is never in the URL.
func redactURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		if u, perr := url.Parse(uerr.URL); perr == nil {
			u.RawQuery = ""
			return &url.Error{Op: uerr.Op, URL: u.String(), Err: uerr.Err}
		}
	}
	return err
}
