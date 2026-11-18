package toolclient

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/gen/syntra/v1/syntrav1connect"
)

type Candidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type Clarification struct {
	Kind       string      `json:"kind"`
	Query      string      `json:"query"`
	Complete   bool        `json:"complete"`
	Candidates []Candidate `json:"candidates"`
}

func (c *Clarification) Error() string {
	return "explicit selection required; directory is ambiguous, absent or incomplete"
}

// Selection identifies one explicitly chosen stable ID or a human name. Empty
// project/team/recipient are absent, never fabricated catalog entries.
type Selection struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Team      string `json:"team,omitempty"`
	Recipient string `json:"recipient,omitempty"`
}
type Resolution struct {
	WorkspaceID             string  `json:"workspaceId"`
	ProjectID               *string `json:"projectId,omitempty"`
	TeamID                  *string `json:"teamId,omitempty"`
	RecipientID             *string `json:"recipientId,omitempty"`
	OriginalRecipientIntent string  `json:"originalRecipientIntent"`
}

// pages restarts ONCE on an expired cursor; partial output never proves unique.
// Every page is server-authorized. Repeated cursors/IDs and bounded overflow fail.
func pages[T any](ctx context.Context, fetch func(context.Context, string) ([]T, string, error), id func(T) string) ([]T, error) {
	for restart := 0; restart < 2; restart++ {
		var all []T
		seen := map[string]bool{}
		tokens := map[string]bool{}
		token := ""
		totalBytes := 0
		retry := false
		for n := 0; n < 1000; n++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			rows, next, e := fetch(ctx, token)
			if e != nil {
				if token != "" && restart == 0 && (connect.CodeOf(e) == connect.CodeFailedPrecondition || connect.CodeOf(e) == connect.CodeInvalidArgument) {
					retry = true
					break
				}
				return nil, errors.New("directory incomplete; refresh or use explicit clarification")
			}
			for _, r := range rows {
				m, ok := any(r).(proto.Message)
				if !ok || m == nil {
					return nil, errors.New("invalid directory projection")
				}
				totalBytes += proto.Size(m)
				if totalBytes > 8*maxResponseBytes {
					return nil, errors.New("directory exceeds aggregate byte bound; incomplete")
				}
				key := id(r)
				if !validUUID(key) || seen[key] {
					return nil, errors.New("directory inconsistent; explicit refresh required")
				}
				seen[key] = true
				all = append(all, r)
				if len(all) > 10000 {
					return nil, errors.New("directory exceeds safe completion bound")
				}
			}
			if next == "" {
				return all, nil
			}
			if len(next) > 2048 || tokens[next] {
				return nil, errors.New("directory cursor repeated or invalid")
			}
			tokens[next] = true
			token = next
		}
		if !retry {
			return nil, errors.New("directory incomplete; page limit")
		}
	}
	return nil, errors.New("directory expired during bounded refresh")
}
func (a *App) Workspaces(ctx context.Context) ([]*v1.AccessibleWorkspace, error) {
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	return pages(ctx, func(ctx context.Context, t string) ([]*v1.AccessibleWorkspace, string, error) {
		r, e := a.client.Tools().ListAccessibleWorkspaces(ctx, bearer(connect.NewRequest(&v1.ListAccessibleWorkspacesRequest{PageSize: 100, PageToken: t}), a.profile.Secret))
		if e != nil {
			return nil, "", e
		}
		for _, w := range r.Msg.Workspaces {
			if w == nil || w.MemberUserId != a.profile.AccountID {
				return nil, "", ErrProfile
			}
		}
		return r.Msg.Workspaces, r.Msg.NextPageToken, nil
	}, func(w *v1.AccessibleWorkspace) string { return w.WorkspaceId })
}
func (a *App) Projects(ctx context.Context, w string) ([]*v1.Project, error) {
	if !validUUID(w) {
		return nil, ciInput()
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	c := syntrav1connect.NewProjectServiceClient(a.client, a.client.origin, RPCOptions()...)
	return pages(ctx, func(ctx context.Context, t string) ([]*v1.Project, string, error) {
		r, e := c.ListProjects(ctx, bearer(connect.NewRequest(&v1.ListProjectsRequest{WorkspaceId: w, PageSize: 100, PageToken: t}), a.profile.Secret))
		if e != nil {
			return nil, "", e
		}
		for _, p := range r.Msg.Projects {
			if p == nil || p.WorkspaceId != w {
				return nil, "", ErrProfile
			}
		}
		return r.Msg.Projects, r.Msg.NextPageToken, nil
	}, func(p *v1.Project) string { return p.Id })
}

// ProjectTeams lists the teams assigned to a project with their effective members.
func (a *App) ProjectTeams(ctx context.Context, w, p string) ([]*v1.ProjectTeam, error) {
	if !validUUID(w) || !validUUID(p) {
		return nil, ciInput()
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	c := syntrav1connect.NewProjectTeamServiceClient(a.client, a.client.origin, RPCOptions()...)
	return pages(ctx, func(ctx context.Context, t string) ([]*v1.ProjectTeam, string, error) {
		r, e := c.ListProjectTeams(ctx, bearer(connect.NewRequest(&v1.ListProjectTeamsRequest{WorkspaceId: w, ProjectId: p, PageSize: 100, PageToken: t}), a.profile.Secret))
		if e != nil {
			return nil, "", e
		}
		for _, m := range r.Msg.ProjectTeams {
			if m == nil || m.WorkspaceId != w || m.ProjectId != p {
				return nil, "", ErrProfile
			}
			for _, member := range m.Members {
				if member == nil || !validUUID(member.UserId) {
					return nil, "", ErrProfile
				}
			}
			// The CLI and MCP only need the effective roster. Keep project
			// administration metadata out of their discovery output.
			m.ExcludedUserIds = nil
		}
		return r.Msg.ProjectTeams, r.Msg.NextPageToken, nil
	}, func(m *v1.ProjectTeam) string { return m.TeamId })
}
func (a *App) Members(ctx context.Context, w string) ([]*v1.WorkspaceMember, error) {
	if !validUUID(w) {
		return nil, ciInput()
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	c := syntrav1connect.NewWorkspaceServiceClient(a.client, a.client.origin, RPCOptions()...)
	return pages(ctx, func(ctx context.Context, t string) ([]*v1.WorkspaceMember, string, error) {
		r, e := c.ListMembers(ctx, bearer(connect.NewRequest(&v1.ListMembersRequest{WorkspaceId: w, PageSize: 100, PageToken: t}), a.profile.Secret))
		if e != nil {
			return nil, "", e
		}
		for _, m := range r.Msg.Members {
			if m == nil {
				return nil, "", ErrProfile
			}
		}
		return r.Msg.Members, r.Msg.NextPageToken, nil
	}, func(m *v1.WorkspaceMember) string { return m.UserId })
}
func ciInput() error { return errors.New("invalid stable routing ID") }
func choose(kind, q string, candidates []Candidate, err error) (string, error) {
	matches := []Candidate{}
	for _, c := range candidates {
		if c.ID == q || strings.EqualFold(c.Name, q) {
			matches = append(matches, c)
		}
	}
	if err != nil {
		return "", &Clarification{kind, q, false, candidates}
	}
	if len(matches) != 1 {
		return "", &Clarification{kind, q, true, matches}
	}
	return matches[0].ID, nil
}
func (a *App) Resolve(ctx context.Context, s Selection) (*Resolution, error) {
	if s.Workspace == "" || s.Team != "" && s.Project == "" {
		return nil, errors.New("workspace required; team requires project")
	}
	ws, e := a.Workspaces(ctx)
	c := []Candidate{}
	for _, x := range ws {
		c = append(c, Candidate{x.WorkspaceId, x.Name, "workspace"})
	}
	w, e := choose("workspace", s.Workspace, c, e)
	if e != nil {
		return nil, e
	}
	r := &Resolution{WorkspaceID: w, OriginalRecipientIntent: s.Recipient}
	var teams []*v1.ProjectTeam
	var teamsErr error
	if s.Project != "" {
		xs, e := a.Projects(ctx, w)
		c = nil
		for _, x := range xs {
			c = append(c, Candidate{x.Id, x.Name, "project"})
		}
		id, e := choose("project", s.Project, c, e)
		if e != nil {
			return nil, e
		}
		r.ProjectID = &id
		if s.Team != "" || s.Recipient != "" {
			teams, teamsErr = a.ProjectTeams(ctx, w, id)
		}
	}
	var team *v1.ProjectTeam
	if s.Team != "" {
		c = nil
		for _, x := range teams {
			c = append(c, Candidate{x.TeamId, x.TeamName, "team"})
		}
		id, e := choose("team", s.Team, c, teamsErr)
		if e != nil {
			var clarification *Clarification
			if errors.As(e, &clarification) && clarification.Complete && len(clarification.Candidates) == 0 {
				return nil, &NotFound{Kind: "team", Query: s.Team, Scope: "project", ScopeID: *r.ProjectID}
			}
			return nil, e
		}
		for _, x := range teams {
			if x.TeamId == id {
				team = x
			}
		}
		r.TeamID = &id
	}
	if s.Recipient != "" {
		c = nil
		var recipientErr error
		switch {
		case teamsErr != nil:
			recipientErr = teamsErr
		case team != nil:
			c = rosterCandidates([]*v1.ProjectTeam{team})
		case len(teams) > 0:
			c = rosterCandidates(teams)
		default:
			xs, e := a.Members(ctx, w)
			recipientErr = e
			for _, x := range xs {
				c = append(c, Candidate{x.UserId, x.DisplayName, "member"})
			}
		}
		id, e := choose("member", s.Recipient, c, recipientErr)
		if e != nil {
			return nil, e
		}
		r.RecipientID = &id
	}
	return r, nil
}

// NotFound reports a selector that matches nothing inside an otherwise complete scope.
type NotFound struct{ Kind, Query, Scope, ScopeID string }

func (n *NotFound) Error() string {
	return n.Kind + " " + strconv.Quote(n.Query) + " is not assigned to " + n.Scope + " " + n.ScopeID
}

// rosterCandidates returns the distinct ACTIVE roster members of the given project teams.
func rosterCandidates(teams []*v1.ProjectTeam) []Candidate {
	out := []Candidate{}
	seen := map[string]bool{}
	for _, t := range teams {
		for _, m := range t.Members {
			if m.Active && !seen[m.UserId] {
				seen[m.UserId] = true
				out = append(out, Candidate{m.UserId, m.DisplayName, "member"})
			}
		}
	}
	return out
}

// Tickets returns complete authorized listing (private visibility remains server
// enforced). Mutation payloads never infer CAS from these mutable projections.
func (a *App) Tickets(ctx context.Context, w string, view v1.TicketVisibility) ([]*v1.Ticket, error) {
	if !validUUID(w) || (view != v1.TicketVisibility_TICKET_VISIBILITY_PUBLISHED && view != v1.TicketVisibility_TICKET_VISIBILITY_OWN_DRAFTS) {
		return nil, ciInput()
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	c := syntrav1connect.NewTicketServiceClient(a.client, a.client.origin, RPCOptions()...)
	return pages(ctx, func(ctx context.Context, t string) ([]*v1.Ticket, string, error) {
		r, e := c.ListTickets(ctx, bearer(connect.NewRequest(&v1.ListTicketsRequest{WorkspaceId: w, Visibility: view, PageSize: 100, PageToken: t}), a.profile.Secret))
		if e != nil {
			return nil, "", e
		}
		for _, x := range r.Msg.Tickets {
			if x == nil || x.WorkspaceId != w {
				return nil, "", ErrProfile
			}
		}
		return r.Msg.Tickets, r.Msg.NextPageToken, nil
	}, func(x *v1.Ticket) string { return x.Id })
}
func (a *App) Ticket(ctx context.Context, w, t string) (*v1.Ticket, error) {
	if !validUUID(w) || !validUUID(t) {
		return nil, ciInput()
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	c := syntrav1connect.NewTicketServiceClient(a.client, a.client.origin, RPCOptions()...)
	r, e := c.GetTicket(ctx, bearer(connect.NewRequest(&v1.GetTicketRequest{WorkspaceId: w, TicketId: t}), a.profile.Secret))
	if e != nil {
		return nil, SafeRPCError(e)
	}
	if r.Msg.Ticket == nil || r.Msg.Ticket.Id != t || r.Msg.Ticket.WorkspaceId != w {
		return nil, ErrProfile
	}
	return r.Msg.Ticket, nil
}

// Messages exposes complete read-only published history OR own private drafts.
// Selecting a view is explicit; neither view sets a read/review receipt.
func (a *App) Messages(ctx context.Context, w, t string, view v1.MessageView) (*v1.ListMessagesResponse, error) {
	if !validUUID(w) || !validUUID(t) || (view != v1.MessageView_MESSAGE_VIEW_PUBLISHED && view != v1.MessageView_MESSAGE_VIEW_OWN_DRAFTS) {
		return nil, ciInput()
	}
	if e := a.authorize(ctx, v1.ToolCapability_TOOL_CAPABILITY_READ); e != nil {
		return nil, e
	}
	client := syntrav1connect.NewMessageServiceClient(a.client, a.client.origin, RPCOptions()...)
	rows, e := pages[proto.Message](ctx, func(ctx context.Context, token string) ([]proto.Message, string, error) {
		r, e := client.ListMessages(ctx, bearer(connect.NewRequest(&v1.ListMessagesRequest{WorkspaceId: w, TicketId: t, View: view, PageSize: 100, PageToken: token}), a.profile.Secret))
		if e != nil {
			return nil, "", e
		}
		out := []proto.Message{}
		for _, m := range r.Msg.Messages {
			if m == nil || m.WorkspaceId != w || m.TicketId != t || view != v1.MessageView_MESSAGE_VIEW_PUBLISHED {
				return nil, "", ErrProfile
			}
			out = append(out, m)
		}
		for _, d := range r.Msg.Drafts {
			if d == nil || d.WorkspaceId != w || d.TicketId != t || d.AuthorUserId != a.profile.AccountID || view != v1.MessageView_MESSAGE_VIEW_OWN_DRAFTS {
				return nil, "", ErrProfile
			}
			out = append(out, d)
		}
		return out, r.Msg.NextPageToken, nil
	}, func(m proto.Message) string {
		switch x := m.(type) {
		case *v1.PublishedMessage:
			return x.Id
		case *v1.MessageDraft:
			return x.Id
		}
		return ""
	})
	if e != nil {
		return nil, e
	}
	out := &v1.ListMessagesResponse{}
	for _, m := range rows {
		switch x := m.(type) {
		case *v1.PublishedMessage:
			out.Messages = append(out.Messages, x)
		case *v1.MessageDraft:
			out.Drafts = append(out.Drafts, x)
		}
	}
	return out, nil
}
