package asana

// Fields requested via opt_fields. Keeping them explicit makes list responses
// carry everything we store, so no per-resource detail requests are needed.
const (
	userFields    = "name,email"
	projectFields = "name,archived,completed,color,created_at,modified_at,due_on,start_on," +
		"privacy_setting,permalink_url,owner,team,team.name,workspace,workspace.name"
)

// Ref is a compact reference to another Asana object.
type Ref struct {
	GID  string `json:"gid"`
	Name string `json:"name,omitempty"`
}

// User is the exported user record. Email is null when Asana does not
// expose it to the authenticated account.
type User struct {
	GID   string  `json:"gid"`
	Name  string  `json:"name"`
	Email *string `json:"email"`
}

// Project is the exported project record. Optional or unset values are null.
// Timestamps and dates are kept exactly as Asana returns them.
type Project struct {
	GID            string  `json:"gid"`
	Name           string  `json:"name"`
	Archived       bool    `json:"archived"`
	Completed      bool    `json:"completed"`
	Color          *string `json:"color"`
	CreatedAt      *string `json:"created_at"`
	ModifiedAt     *string `json:"modified_at"`
	DueOn          *string `json:"due_on"`
	StartOn        *string `json:"start_on"`
	PrivacySetting *string `json:"privacy_setting"`
	PermalinkURL   *string `json:"permalink_url"`
	Owner          *Ref    `json:"owner"`
	Team           *Ref    `json:"team"`
	Workspace      *Ref    `json:"workspace"`
}

type page[T any] struct {
	Data     []T `json:"data"`
	NextPage *struct {
		Offset string `json:"offset"`
	} `json:"next_page"`
}
