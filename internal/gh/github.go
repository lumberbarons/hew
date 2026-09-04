package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/lumberbarons/hew/internal/model"
)

// Nested connections don't paginate with the outer issues cursor, so they
// are capped; totals are carried so the read path can warn on truncation.
const (
	issuePageSize = 100
	subIssueCap   = 50
	blockerCap    = 20
	commentCap    = 5
	labelCap      = 50
	assigneeCap   = 10
	// searchCap bounds search results deliberately: search exists to answer
	// "does an issue for this already exist?", where best-match rank matters
	// and an exhaustive page-through does not.
	searchCap = 20
)

// GitHub implements Client against the real API via go-gh.
type GitHub struct {
	repo Repo
	gql  *api.GraphQLClient
	rest *api.RESTClient
	// nodeIDs memoizes issue number → GraphQL node ID for this repo. Node
	// IDs are immutable, so entries never invalidate. Not synchronized: a
	// client serves one CLI command on one goroutine.
	nodeIDs map[int]string
}

// New builds a client using gh's stored credentials and host config.
func New(repo Repo) (*GitHub, error) {
	return NewWithOptions(repo, api.ClientOptions{})
}

// NewWithOptions exists for tests, which inject a Transport.
func NewWithOptions(repo Repo, opts api.ClientOptions) (*GitHub, error) {
	gql, err := api.NewGraphQLClient(opts)
	if err != nil {
		return nil, &AuthError{Err: err}
	}
	rest, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, &AuthError{Err: err}
	}
	return &GitHub{repo: repo, gql: gql, rest: rest, nodeIDs: map[int]string{}}, nil
}

func (g *GitHub) Viewer(ctx context.Context) (string, error) {
	var resp struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	if err := g.gql.DoWithContext(ctx, `query { viewer { login } }`, nil, &resp); err != nil {
		return "", wrapErr(err)
	}
	return resp.Viewer.Login, nil
}

// issueFields is the shared GraphQL selection for both list and get. Body
// rides along everywhere: it's a scalar, so it adds no rate-limit cost, and
// list --bodies needs it without a second query per issue.
var issueFields = fmt.Sprintf(`
	id number title state stateReason createdAt body
	labels(first: %d) { nodes { name } }
	assignees(first: %d) { nodes { login } }
	parent { number state title }
	subIssues(first: %d) { totalCount nodes { number state } }
	subIssuesSummary { total completed }
	blockedBy(first: %d) { totalCount nodes { number state } }`,
	labelCap, assigneeCap, subIssueCap, blockerCap)

type issueNode struct {
	ID          string    `json:"id"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	State       string    `json:"state"`
	StateReason string    `json:"stateReason"`
	CreatedAt   time.Time `json:"createdAt"`
	Body        string    `json:"body"`
	Labels      struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	Parent *struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Title  string `json:"title"`
	} `json:"parent"`
	SubIssues struct {
		TotalCount int       `json:"totalCount"`
		Nodes      []refNode `json:"nodes"`
	} `json:"subIssues"`
	SubIssuesSummary struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
	} `json:"subIssuesSummary"`
	BlockedBy struct {
		TotalCount int       `json:"totalCount"`
		Nodes      []refNode `json:"nodes"`
	} `json:"blockedBy"`
	Comments struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Author *struct {
				Login string `json:"login"`
			} `json:"author"`
			CreatedAt time.Time `json:"createdAt"`
			Body      string    `json:"body"`
		} `json:"nodes"`
	} `json:"comments"`
}

type refNode struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

func (n issueNode) toModel() model.Issue {
	i := model.Issue{
		ID:                 n.ID,
		Number:             n.Number,
		Title:              n.Title,
		Body:               n.Body,
		State:              n.State,
		StateReason:        n.StateReason,
		CreatedAt:          n.CreatedAt,
		SubIssuesTotal:     n.SubIssues.TotalCount,
		SubIssuesCompleted: n.SubIssuesSummary.Completed,
		BlockedByTotal:     n.BlockedBy.TotalCount,
		CommentsTotal:      n.Comments.TotalCount,
	}
	for _, l := range n.Labels.Nodes {
		i.Labels = append(i.Labels, l.Name)
	}
	for _, a := range n.Assignees.Nodes {
		i.Assignees = append(i.Assignees, a.Login)
	}
	if n.Parent != nil {
		i.Parent = &model.Ref{Number: n.Parent.Number, State: n.Parent.State}
		i.ParentTitle = n.Parent.Title
	}
	for _, s := range n.SubIssues.Nodes {
		i.SubIssues = append(i.SubIssues, model.Ref{Number: s.Number, State: s.State})
	}
	for _, b := range n.BlockedBy.Nodes {
		i.BlockedBy = append(i.BlockedBy, model.Ref{Number: b.Number, State: b.State})
	}
	for _, c := range n.Comments.Nodes {
		author := ""
		if c.Author != nil {
			author = c.Author.Login
		}
		i.Comments = append(i.Comments, model.Comment{Author: author, CreatedAt: c.CreatedAt, Body: c.Body})
	}
	return i
}

func (g *GitHub) ListIssues(ctx context.Context, states []IssueState) ([]model.Issue, error) {
	query := fmt.Sprintf(`
	query($owner: String!, $name: String!, $states: [IssueState!], $cursor: String) {
		repository(owner: $owner, name: $name) {
			issues(states: $states, first: %d, after: $cursor) {
				pageInfo { hasNextPage endCursor }
				nodes {%s}
			}
		}
	}`, issuePageSize, issueFields)

	var out []model.Issue
	var cursor *string
	for {
		var resp struct {
			Repository struct {
				Issues struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []issueNode `json:"nodes"`
				} `json:"issues"`
			} `json:"repository"`
		}
		vars := map[string]any{
			"owner":  g.repo.Owner,
			"name":   g.repo.Name,
			"states": states,
			"cursor": cursor,
		}
		if err := g.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
			return nil, wrapErr(err)
		}
		for _, n := range resp.Repository.Issues.Nodes {
			out = append(out, n.toModel())
		}
		if !resp.Repository.Issues.PageInfo.HasNextPage {
			return out, nil
		}
		c := resp.Repository.Issues.PageInfo.EndCursor
		cursor = &c
	}
}

func (g *GitHub) GetIssue(ctx context.Context, number int) (model.Issue, error) {
	query := fmt.Sprintf(`
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			issue(number: $number) {%s
				comments(last: %d) { totalCount nodes { author { login } createdAt body } }
			}
		}
	}`, issueFields, commentCap)

	var resp struct {
		Repository struct {
			Issue *issueNode `json:"issue"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": g.repo.Owner, "name": g.repo.Name, "number": number}
	if err := g.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
		if nf := g.notFoundError(err, number); nf != nil {
			return model.Issue{}, nf
		}
		return model.Issue{}, wrapErr(err)
	}
	if resp.Repository.Issue == nil {
		return model.Issue{}, fmt.Errorf("issue #%d not found in %s", number, g.repo)
	}
	return resp.Repository.Issue.toModel(), nil
}

func (g *GitHub) SearchIssues(ctx context.Context, terms string) ([]model.Issue, int, error) {
	query := fmt.Sprintf(`
	query($q: String!) {
		search(type: ISSUE, query: $q, first: %d) {
			issueCount
			nodes { ... on Issue {%s} }
		}
	}`, searchCap, issueFields)

	var resp struct {
		Search struct {
			IssueCount int         `json:"issueCount"`
			Nodes      []issueNode `json:"nodes"`
		} `json:"search"`
	}
	// The repo scope and is:issue ride in the search string itself — the
	// search API has no structured equivalents. User terms follow, so
	// qualifiers like label:bug pass through.
	q := fmt.Sprintf("repo:%s/%s is:issue %s", g.repo.Owner, g.repo.Name, terms)
	if err := g.gql.DoWithContext(ctx, query, map[string]any{"q": q}, &resp); err != nil {
		return nil, 0, wrapErr(err)
	}
	var out []model.Issue
	for _, n := range resp.Search.Nodes {
		// The inline fragment leaves non-issue nodes (a pull request matched
		// via user-supplied qualifiers) empty; drop them.
		if n.Number == 0 {
			continue
		}
		out = append(out, n.toModel())
	}
	return out, resp.Search.IssueCount, nil
}

// notFoundError classifies a GraphQL NOT_FOUND error by the field it points
// at, so a bad --repo reports the repository as the problem rather than
// blaming a missing issue. It returns nil when err isn't a NOT_FOUND we
// recognise, leaving the caller to fall back to wrapErr. Classification is
// on the structured Type/Path fields, not the server's prose, so a
// GitHub-side rewording can't silently break it.
func (g *GitHub) notFoundError(err error, number int) error {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) {
		return nil
	}
	for _, e := range gqlErr.Errors {
		if e.Type != "NOT_FOUND" {
			continue
		}
		// The repository node fails to resolve at path ["repository"];
		// a missing issue fails deeper, at ["repository", "issue"].
		if len(e.Path) == 1 && pathSegment(e.Path[0]) == "repository" {
			return fmt.Errorf("repository %s not found or not accessible", g.repo)
		}
		return fmt.Errorf("issue #%d not found in %s", number, g.repo)
	}
	return nil
}

func pathSegment(v any) string {
	s, _ := v.(string)
	return s
}

func (g *GitHub) CreateIssue(ctx context.Context, title, body string, labels []string) (model.Issue, error) {
	payload := map[string]any{"title": title, "labels": labels}
	if body != "" {
		payload["body"] = body
	}
	var resp struct {
		NodeID string `json:"node_id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if err := g.restDo(ctx, "POST", g.issuesPath(""), payload, &resp); err != nil {
		return model.Issue{}, err
	}
	return model.Issue{ID: resp.NodeID, Number: resp.Number, Title: resp.Title, State: "OPEN", Labels: labels}, nil
}

func (g *GitHub) EditTitle(ctx context.Context, number int, title string) error {
	return g.restDo(ctx, "PATCH", g.issuePath(number, ""), map[string]any{"title": title}, nil)
}

func (g *GitHub) EditBody(ctx context.Context, number int, body string) error {
	return g.restDo(ctx, "PATCH", g.issuePath(number, ""), map[string]any{"body": body}, nil)
}

func (g *GitHub) AddLabels(ctx context.Context, number int, labels []string) error {
	return g.restDo(ctx, "POST", g.issuePath(number, "/labels"), map[string]any{"labels": labels}, nil)
}

func (g *GitHub) RemoveLabel(ctx context.Context, number int, label string) error {
	err := g.restDo(ctx, "DELETE", g.issuePath(number, "/labels/"+url.PathEscape(label)), nil, nil)
	// Removing an absent label is a no-op, not an error.
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		return nil
	}
	return err
}

func (g *GitHub) AddAssignee(ctx context.Context, number int, login string) error {
	return g.restDo(ctx, "POST", g.issuePath(number, "/assignees"), map[string]any{"assignees": []string{login}}, nil)
}

func (g *GitHub) RemoveAssignees(ctx context.Context, number int, logins []string) error {
	return g.restDo(ctx, "DELETE", g.issuePath(number, "/assignees"), map[string]any{"assignees": logins}, nil)
}

func (g *GitHub) Comment(ctx context.Context, number int, body string) error {
	return g.restDo(ctx, "POST", g.issuePath(number, "/comments"), map[string]any{"body": body}, nil)
}

// nodeID resolves an issue number to its GraphQL node ID, which the mutation
// APIs need. It lets callers work purely in issue numbers. A missing issue or
// repository is classified the same way GetIssue does. Resolutions are
// memoized, so a command that mutates several edges sharing an endpoint
// (e.g. migrate wiring one parent to many children) pays one lookup per
// distinct issue, not per edge.
func (g *GitHub) nodeID(ctx context.Context, number int) (string, error) {
	if id, ok := g.nodeIDs[number]; ok {
		return id, nil
	}
	query := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { issue(number: $number) { id } }
	}`
	var resp struct {
		Repository struct {
			Issue *struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": g.repo.Owner, "name": g.repo.Name, "number": number}
	if err := g.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
		if nf := g.notFoundError(err, number); nf != nil {
			return "", nf
		}
		return "", wrapErr(err)
	}
	if resp.Repository.Issue == nil {
		return "", fmt.Errorf("issue #%d not found in %s", number, g.repo)
	}
	g.nodeIDs[number] = resp.Repository.Issue.ID
	return resp.Repository.Issue.ID, nil
}

func (g *GitHub) CloseIssue(ctx context.Context, number int, reason CloseReason) error {
	id, err := g.nodeID(ctx, number)
	if err != nil {
		return err
	}
	// The reason is passed as a typed GraphQL variable, not interpolated into
	// the query, so a bad value is a validation error naming the field rather
	// than an opaque parse error.
	query := `
	mutation($id: ID!, $reason: IssueClosedStateReason!) {
		closeIssue(input: {issueId: $id, stateReason: $reason}) { clientMutationId }
	}`
	vars := map[string]any{"id": id, "reason": string(reason)}
	return wrapErr(g.gql.DoWithContext(ctx, query, vars, &struct{}{}))
}

func (g *GitHub) ReopenIssue(ctx context.Context, number int) error {
	id, err := g.nodeID(ctx, number)
	if err != nil {
		return err
	}
	query := `
	mutation($id: ID!) {
		reopenIssue(input: {issueId: $id}) { clientMutationId }
	}`
	vars := map[string]any{"id": id}
	return wrapErr(g.gql.DoWithContext(ctx, query, vars, &struct{}{}))
}

func (g *GitHub) AddBlockedBy(ctx context.Context, number, blockingNumber int) error {
	return g.dependencyMutation(ctx, "addBlockedBy", number, blockingNumber)
}

func (g *GitHub) RemoveBlockedBy(ctx context.Context, number, blockingNumber int) error {
	return g.dependencyMutation(ctx, "removeBlockedBy", number, blockingNumber)
}

func (g *GitHub) dependencyMutation(ctx context.Context, name string, number, blockingNumber int) error {
	id, err := g.nodeID(ctx, number)
	if err != nil {
		return err
	}
	blockingID, err := g.nodeID(ctx, blockingNumber)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`
	mutation($id: ID!, $blocking: ID!) {
		%s(input: {issueId: $id, blockingIssueId: $blocking}) { clientMutationId }
	}`, name)
	vars := map[string]any{"id": id, "blocking": blockingID}
	return wrapErr(g.gql.DoWithContext(ctx, query, vars, &struct{}{}))
}

func (g *GitHub) AddSubIssue(ctx context.Context, parentNumber, childNumber int, replaceParent bool) error {
	parentID, err := g.nodeID(ctx, parentNumber)
	if err != nil {
		return err
	}
	childID, err := g.nodeID(ctx, childNumber)
	if err != nil {
		return err
	}
	query := `
	mutation($parent: ID!, $child: ID!, $replace: Boolean) {
		addSubIssue(input: {issueId: $parent, subIssueId: $child, replaceParent: $replace}) { clientMutationId }
	}`
	vars := map[string]any{"parent": parentID, "child": childID, "replace": replaceParent}
	return wrapErr(g.gql.DoWithContext(ctx, query, vars, &struct{}{}))
}

func (g *GitHub) RemoveSubIssue(ctx context.Context, parentNumber, childNumber int) error {
	parentID, err := g.nodeID(ctx, parentNumber)
	if err != nil {
		return err
	}
	childID, err := g.nodeID(ctx, childNumber)
	if err != nil {
		return err
	}
	query := `
	mutation($parent: ID!, $child: ID!) {
		removeSubIssue(input: {issueId: $parent, subIssueId: $child}) { clientMutationId }
	}`
	vars := map[string]any{"parent": parentID, "child": childID}
	return wrapErr(g.gql.DoWithContext(ctx, query, vars, &struct{}{}))
}

func (g *GitHub) ListLabels(ctx context.Context) ([]Label, error) {
	var out []Label
	page := 1
	for {
		var resp []struct {
			Name        string `json:"name"`
			Color       string `json:"color"`
			Description string `json:"description"`
		}
		path := fmt.Sprintf("repos/%s/%s/labels?per_page=100&page=%d", g.repo.Owner, g.repo.Name, page)
		if err := g.restDo(ctx, "GET", path, nil, &resp); err != nil {
			return nil, err
		}
		for _, l := range resp {
			out = append(out, Label{Name: l.Name, Color: l.Color, Description: l.Description})
		}
		if len(resp) < 100 {
			return out, nil
		}
		page++
	}
}

func (g *GitHub) CreateLabel(ctx context.Context, label Label) error {
	payload := map[string]any{"name": label.Name, "color": label.Color, "description": label.Description}
	return g.restDo(ctx, "POST", fmt.Sprintf("repos/%s/%s/labels", g.repo.Owner, g.repo.Name), payload, nil)
}

func (g *GitHub) PullRequestContext(ctx context.Context, head string) (PRContext, error) {
	// One query: the base branch to target and the open PR already on this
	// head. headRefName filters server-side, so "first: 1" is the answer,
	// not a sample.
	query := `
	query($owner: String!, $name: String!, $head: String!) {
		repository(owner: $owner, name: $name) {
			defaultBranchRef { name }
			pullRequests(headRefName: $head, states: OPEN, first: 1) {
				nodes { number url isDraft }
			}
		}
	}`
	var resp struct {
		Repository struct {
			DefaultBranchRef *struct {
				Name string `json:"name"`
			} `json:"defaultBranchRef"`
			PullRequests struct {
				Nodes []struct {
					Number  int    `json:"number"`
					URL     string `json:"url"`
					IsDraft bool   `json:"isDraft"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": g.repo.Owner, "name": g.repo.Name, "head": head}
	if err := g.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
		return PRContext{}, wrapErr(err)
	}
	var out PRContext
	if ref := resp.Repository.DefaultBranchRef; ref != nil {
		out.DefaultBranch = ref.Name
	}
	if nodes := resp.Repository.PullRequests.Nodes; len(nodes) > 0 {
		out.Existing = &PullRequest{Number: nodes[0].Number, URL: nodes[0].URL, Draft: nodes[0].IsDraft}
	}
	return out, nil
}

func (g *GitHub) CreatePullRequest(ctx context.Context, pr NewPullRequest) (PullRequest, error) {
	payload := map[string]any{
		"title": pr.Title, "body": pr.Body,
		"head": pr.Head, "base": pr.Base, "draft": pr.Draft,
	}
	var resp struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
	}
	path := fmt.Sprintf("repos/%s/%s/pulls", g.repo.Owner, g.repo.Name)
	if err := g.restDo(ctx, "POST", path, payload, &resp); err != nil {
		return PullRequest{}, err
	}
	return PullRequest{Number: resp.Number, URL: resp.HTMLURL, Draft: resp.Draft}, nil
}

func (g *GitHub) issuesPath(suffix string) string {
	return fmt.Sprintf("repos/%s/%s/issues%s", g.repo.Owner, g.repo.Name, suffix)
}

func (g *GitHub) issuePath(number int, suffix string) string {
	return fmt.Sprintf("repos/%s/%s/issues/%d%s", g.repo.Owner, g.repo.Name, number, suffix)
}

func (g *GitHub) restDo(ctx context.Context, method, path string, payload, resp any) error {
	var body *bytes.Buffer
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewBuffer(b)
	} else {
		body = &bytes.Buffer{}
	}
	return wrapErr(g.rest.DoWithContext(ctx, method, path, body, resp))
}
