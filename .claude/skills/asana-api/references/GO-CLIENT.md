# Go stdlib client patterns for Asana

Reference snippets. Adapt package names to the project layout in `.ai-factory/ARCHITECTURE.md`.

## Models

```go
type Ref struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type User struct {
	GID        string `json:"gid"`
	Name       string `json:"name"`
	Email      string `json:"email,omitempty"`
	Workspaces []Ref  `json:"workspaces,omitempty"`
}

type Project struct {
	GID          string    `json:"gid"`
	Name         string    `json:"name"`
	Archived     bool      `json:"archived"`
	CreatedAt    time.Time `json:"created_at"`
	ModifiedAt   time.Time `json:"modified_at"`
	Owner        *Ref      `json:"owner"`
	Team         *Ref      `json:"team"`
	Workspace    Ref       `json:"workspace"`
	PermalinkURL string    `json:"permalink_url"`
}

type nextPage struct {
	Offset string `json:"offset"`
}

type page[T any] struct {
	Data     []T       `json:"data"`
	NextPage *nextPage `json:"next_page"`
}
```

## Typed API error

```go
type APIError struct {
	Status  int
	Message string
	Phrase  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("asana: %d: %s", e.Status, e.Message)
}

func (e *APIError) NotAccessible() bool {
	return e.Status == http.StatusForbidden || e.Status == http.StatusNotFound
}

func decodeAPIError(resp *http.Response) error {
	var body struct {
		Errors []struct {
			Message string `json:"message"`
			Phrase  string `json:"phrase"`
		} `json:"errors"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body)
	e := &APIError{Status: resp.StatusCode}
	if len(body.Errors) > 0 {
		e.Message = body.Errors[0].Message
		e.Phrase = body.Errors[0].Phrase
	}
	return e
}
```

Callers use `errors.As(err, &apiErr)`: `apiErr.NotAccessible()` → skip, anything else (401 etc.) → abort.

## Client with retry

```go
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	sem     chan struct{} // bounds in-flight GETs
}

func NewClient(token string, concurrency int) *Client {
	return &Client{
		baseURL: "https://app.asana.com/api/1.0",
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
		sem:     make(chan struct{}, concurrency),
	}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	const maxAttempts = 5
	for attempt := 0; ; attempt++ {
		resp, err := c.do(ctx, path, q)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			return json.NewDecoder(resp.Body).Decode(out)
		}

		apiErr := decodeAPIError(resp)
		resp.Body.Close()

		wait, retry := retryDelay(resp, attempt)
		if !retry || attempt+1 >= maxAttempts {
			return apiErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (c *Client) do(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

func retryDelay(resp *http.Response, attempt int) (time.Duration, bool) {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		secs, err := strconv.Atoi(resp.Header.Get("Retry-After"))
		if err != nil || secs <= 0 {
			secs = 30
		}
		return time.Duration(secs) * time.Second, true
	case resp.StatusCode >= 500:
		base := time.Second << attempt
		return base + rand.N(base/2+1), true // math/rand/v2
	default:
		return 0, false
	}
}
```

Note: the semaphore is released before the retry sleep, so waiting requests don't hold concurrency slots.

## Generic paginator (iterator)

```go
func paginate[T any](ctx context.Context, c *Client, path string, q url.Values) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		q := maps.Clone(q)
		q.Set("limit", "100")
		for {
			var p page[T]
			if err := c.get(ctx, path, q, &p); err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range p.Data {
				if !yield(item, nil) {
					return
				}
			}
			if p.NextPage == nil || p.NextPage.Offset == "" {
				return
			}
			q.Set("offset", p.NextPage.Offset)
		}
	}
}

func (c *Client) Users(ctx context.Context, workspaceGID string) iter.Seq2[User, error] {
	q := url.Values{
		"workspace":  {workspaceGID},
		"opt_fields": {"name,email,workspaces.name"},
	}
	return paginate[User](ctx, c, "/users", q)
}

func (c *Client) Projects(ctx context.Context, workspaceGID string) iter.Seq2[Project, error] {
	q := url.Values{
		"workspace":  {workspaceGID},
		"opt_fields": {"name,archived,created_at,modified_at,owner,team.name,workspace.name,permalink_url"},
	}
	return paginate[Project](ctx, c, "/projects", q)
}
```

`url.Values` is a map, so clone it before mutating `offset`.

## Atomic JSON writer

```go
func writeJSON(path string, v any) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
```

For very large workspaces prefer NDJSON: encode each item as it arrives from the iterator instead of collecting a slice.

## Testing

Fake the API with `httptest.NewServer`; point `Client.baseURL` at it. Cover:
- multi-page response (`next_page` → `null`)
- `429` with `Retry-After: 1` then `200`
- `401` aborts without retry
- context cancellation during a Retry-After sleep
