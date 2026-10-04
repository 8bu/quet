package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/8bu/quet/internal/annotate"
)

// slugRE is the syntax of a project slug, as enforced by the server.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// namedJSON is a type or status of the normalized schema JSON.
type namedJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// spanJSON is a span field of the normalized schema JSON.
type spanJSON struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	NullForTypes []string `json:"null_for_types"`
	Statuses     []string `json:"statuses"`
}

// schemaJSON is the normalized schema JSON of the server contract.
type schemaJSON struct {
	Version           *string     `json:"version"`
	Types             []namedJSON `json:"types"`
	Statuses          []namedJSON `json:"statuses"`
	NullLabelStatuses []string    `json:"null_label_statuses"`
	ImplicitTarget    bool        `json:"implicit_target"`
	Spans             []spanJSON  `json:"spans"`
}

// nonNil returns s, or an empty non-nil slice, so it encodes as [] and never null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SchemaJSON returns the normalized schema JSON of the server contract: version null when empty, types, statuses
// and spans in YAML order, the resolved null_label_statuses, implicit_target, and [] (never null) for empty lists.
func SchemaJSON(s *annotate.Schema) (json.RawMessage, error) {
	out := schemaJSON{
		Types:             make([]namedJSON, len(s.Types)),
		Statuses:          make([]namedJSON, len(s.Statuses)),
		NullLabelStatuses: nonNil(s.NullLabelStatuses),
		ImplicitTarget:    s.ImplicitTarget,
		Spans:             make([]spanJSON, len(s.Spans)),
	}
	if s.Version != "" {
		out.Version = &s.Version
	}
	for i, t := range s.Types {
		out.Types[i] = namedJSON(t)
	}
	for i, st := range s.Statuses {
		out.Statuses[i] = namedJSON(st)
	}
	for i, sp := range s.Spans {
		out.Spans[i] = spanJSON{sp.Name, sp.Description, nonNil(sp.NullForTypes), nonNil(sp.Statuses)}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, fmt.Errorf("encode schema: %w", err)
	}
	return json.RawMessage(bytes.TrimSpace(buf.Bytes())), nil
}

// PushInput names the files and target of a push. ProposalsPath "" leaves the server's proposals untouched.
type PushInput struct{ QueuePath, SchemaPath, ProposalsPath, Project, Name string }

// PushResult summarizes a push. Proposals is the number of proposals the server stored (0 when none were pushed).
type PushResult struct {
	Created                      bool
	Inserted, Updated, Unchanged int
	Proposals                    int
	IgnoredProposals             []string
}

// loadProposalLines reads a proposals JSONL file as raw lines: every non-blank line must be a JSON object with a
// string id and a string annotation_status, and ids must be unique. The lines are returned verbatim.
func loadProposalLines(path string) ([]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read proposals: %w", err)
	}
	var raw []json.RawMessage
	firstLine := map[string]int{}
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		lineNo := i + 1
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("proposals %s:%d: invalid JSON object", path, lineNo)
		}
		var id string
		for _, key := range []string{"id", "annotation_status"} {
			// A null or missing member unmarshals into "" without error: require a JSON string token.
			var v string
			if tok := bytes.TrimSpace(fields[key]); len(tok) == 0 || tok[0] != '"' || json.Unmarshal(tok, &v) != nil {
				return nil, fmt.Errorf("proposals %s:%d: field %q must be a string", path, lineNo, key)
			}
			if key == "id" {
				id = v
			}
		}
		if id == "" {
			return nil, fmt.Errorf("proposals %s:%d: empty id", path, lineNo)
		}
		if first, dup := firstLine[id]; dup {
			return nil, fmt.Errorf("proposals %s:%d: duplicate id %q (first on line %d)", path, lineNo, id, first)
		}
		firstLine[id] = lineNo
		raw = append(raw, json.RawMessage(bytes.Clone(line)))
	}
	return raw, nil
}

// Push publishes a project: it validates the schema, queue and proposals locally, then PutProject, PushItems and,
// when ProposalsPath is set, ReplaceProposals. The project name is sent only when in.Name is set or the project
// does not exist yet (then it defaults to the slug); an existing project keeps its name otherwise.
func Push(ctx context.Context, c *Client, in PushInput) (PushResult, error) {
	var res PushResult
	if !slugRE.MatchString(in.Project) {
		return res, fmt.Errorf("invalid project slug %q (want lowercase letters, digits and '-', starting with a letter or digit, at most 63 characters)", in.Project)
	}
	schemaYAML, err := os.ReadFile(in.SchemaPath)
	if err != nil {
		return res, fmt.Errorf("read schema: %w", err)
	}
	schema, err := annotate.ParseSchema(schemaYAML)
	if err != nil {
		return res, fmt.Errorf("schema %s: %w", in.SchemaPath, err)
	}
	schemaRaw, err := SchemaJSON(schema)
	if err != nil {
		return res, err
	}
	items, err := annotate.LoadQueue(in.QueuePath)
	if err != nil {
		return res, err
	}
	var proposals []json.RawMessage
	if in.ProposalsPath != "" {
		if proposals, err = loadProposalLines(in.ProposalsPath); err != nil {
			return res, err
		}
	}

	name := in.Name
	if name == "" {
		if _, err := c.Project(ctx, in.Project); errors.Is(err, ErrNotFound) {
			name = in.Project
		} else if err != nil {
			return res, fmt.Errorf("check project %s: %w", in.Project, err)
		}
	}
	if res.Created, err = c.PutProject(ctx, in.Project, name, schemaRaw, string(schemaYAML)); err != nil {
		return res, fmt.Errorf("push project %s: %w", in.Project, err)
	}
	if res.Inserted, res.Updated, res.Unchanged, err = c.PushItems(ctx, in.Project, items); err != nil {
		return res, fmt.Errorf("push items: %w", err)
	}
	if in.ProposalsPath != "" {
		if res.Proposals, res.IgnoredProposals, err = c.ReplaceProposals(ctx, in.Project, proposals); err != nil {
			return res, fmt.Errorf("push proposals: %w", err)
		}
	}
	return res, nil
}

// Pulled is everything a pull needs: the project, its parsed schema, its items in position order and every pulled
// label decoded and validated against that schema and the item text.
type Pulled struct {
	Project *Project
	Schema  *annotate.Schema
	Items   []annotate.Item
	// Labels maps collaborator → item id → label; only valid labels are in it.
	Labels map[string]map[string]annotate.Label
	// Invalid lists the labels that could not be decoded or failed validation; they are not in Labels.
	Invalid []Invalid
}

// Pull fetches project slug and the labels of collaborator (every collaborator when empty). The schema is parsed
// from the stored schema YAML with Quet's own parser, which is the validator of every pulled label.
func Pull(ctx context.Context, c *Client, slug, collaborator string) (*Pulled, error) {
	project, err := c.Project(ctx, slug)
	if err != nil {
		return nil, err
	}
	schema, err := annotate.ParseSchema([]byte(project.SchemaYAML))
	if err != nil {
		return nil, fmt.Errorf("schema of project %s: %w", slug, err)
	}
	webItems, err := c.Items(ctx, slug)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(webItems, func(i, j int) bool { return webItems[i].Position < webItems[j].Position })
	p := &Pulled{
		Project: project,
		Schema:  schema,
		Items:   make([]annotate.Item, len(webItems)),
		Labels:  map[string]map[string]annotate.Label{},
	}
	text := make(map[string]string, len(webItems))
	for i, it := range webItems {
		p.Items[i] = annotate.Item{ID: it.ID, Text: it.Text}
		text[it.ID] = it.Text
	}
	remote, err := c.Labels(ctx, slug, collaborator)
	if err != nil {
		return nil, err
	}
	for _, rl := range remote {
		l, err := annotate.ParseLabel(schema, rl.Label)
		if err != nil {
			var head struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(rl.Label, &head)
			p.Invalid = append(p.Invalid, Invalid{rl.Collaborator, head.ID, err.Error()})
			continue
		}
		txt, ok := text[l.ID]
		if !ok {
			p.Invalid = append(p.Invalid, Invalid{rl.Collaborator, l.ID, fmt.Sprintf("record %q is not in the project", l.ID)})
			continue
		}
		if err := schema.Validate(l, txt); err != nil {
			p.Invalid = append(p.Invalid, Invalid{rl.Collaborator, l.ID, err.Error()})
			continue
		}
		if p.Labels[rl.Collaborator] == nil {
			p.Labels[rl.Collaborator] = map[string]annotate.Label{}
		}
		p.Labels[rl.Collaborator][l.ID] = l
	}
	return p, nil
}
