package jam

import (
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// Connections in use (intercom slice 1a-4): which connection serves a kind,
// and a project's chat service as a connection.

// ChatKinds are the connection kinds that can back a project's chat service.
var ChatKinds = []string{"discord"}

// connectionOfKind is "the connection of kind k": the live connection named k
// if there is one (the implicit connection 1a-3a created), else the
// lowest-id live connection of kind k. Caller holds mu.
func (m *memState) connectionOfKind(k string) (Connection, bool) {
	if c, ok := m.liveConnectionNamed(k); ok && c.Kind == k {
		return c, true
	}
	var best Connection
	found := false
	for _, c := range m.connections {
		if c.Kind == k && c.Status == StatusLive && (!found || c.ID < best.ID) {
			best, found = c, true
		}
	}
	return best, found
}

// resolveChatService resolves a chat-service ref — a connection id, a
// connection name, or a chat kind naming its implicit connection (created
// when absent) — to the connection to store. Caller holds mu.
func (m *memState) resolveChatService(ref string) (c Connection, created bool, err error) {
	if id, perr := ident.Parse(ref); perr == nil && id.Kind() == ident.Connection {
		c, err = m.liveConnection(id)
	} else if named, ok := m.liveConnectionNamed(ref); ok {
		c = named
	} else if slices.Contains(ChatKinds, ref) {
		if c, ok = m.connectionOfKind(ref); !ok {
			c, created = Connection{ID: ident.New(ident.Connection), Kind: ref, Name: ref, Status: StatusLive}, true
		}
	} else {
		err = fmt.Errorf("%w: %q", ErrConnectionNotFound, ref)
	}
	if err == nil && !slices.Contains(ChatKinds, c.Kind) {
		err = fmt.Errorf("connection %q is a %s connection; a chat service must be one of %v", c.Name, c.Kind, ChatKinds)
	}
	return c, created, err
}

// prepareSetChatService validates SetChatService: the project (DefaultProject
// materializes) and the chat-service ref ("" clears). It returns the project
// to write and any connection to create first. Caller holds mu.
func (m *memState) prepareSetChatService(project, ref string) (Project, []Connection, error) {
	p, _, err := m.requireProject(project)
	if err != nil {
		return Project{}, nil, err
	}
	p = copyProject(p)
	if ref == "" {
		p.ChatService = ""
		return p, nil, nil
	}
	c, created, err := m.resolveChatService(ref)
	if err != nil {
		return Project{}, nil, err
	}
	p.ChatService = string(c.ID)
	if created {
		return p, []Connection{c}, nil
	}
	return p, nil, nil
}

func (m *memState) prepareSetConnectionCred(id ident.ID, cred string) (Connection, error) {
	c, err := m.liveConnection(id)
	if err != nil {
		return Connection{}, err
	}
	c.CredName = cred
	return c, nil
}

// ConnectionGetter is the slice of Store ChatKind reads.
type ConnectionGetter interface {
	GetConnection(id ident.ID) (Connection, bool)
}

// ChatKind is the kind of project's chat-service connection ("discord"), or
// "" when the project has none (tracker @-mentions only) or it was removed.
func ChatKind(store ConnectionGetter, p Project) string {
	if p.ChatService == "" {
		return ""
	}
	c, ok := store.GetConnection(ident.ID(p.ChatService))
	if !ok || c.Status != StatusLive {
		return ""
	}
	return c.Kind
}
