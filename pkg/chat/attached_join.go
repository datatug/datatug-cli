package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dtql"
)

// joinAttachedQuery extends a fresh model-authored single-table query through
// DataTug's existing FK join application. No intermediate result is persisted.
func (c *SessionChat) joinAttachedQuery(ctx context.Context, session ChatSession, prompt string, query QueryResult) (QueryResult, bool, error) {
	application, ok := c.joinApplication.(interface {
		JoinApplication
		ApplyAttached(context.Context, RecordSet, JoinCandidateID) (QueryResult, error)
	})
	if !ok || query.Source != c.source {
		return query, false, nil
	}
	parsed, err := dtql.Deserialize([]byte(query.DTQL))
	if err != nil {
		return query, false, err
	}
	if len(parsed.GroupBy()) > 0 || parsed.Having() != nil || queryHasAggregate(parsed) {
		return query, false, nil
	}
	instances := relationInstances(parsed.From())
	if len(instances) == 0 {
		return query, false, nil
	}
	root := instances[0]
	sourceID := query.SourceID
	if sourceID == "" && c.store != nil {
		sourceID = c.store.info.Database
	}
	var targets []ContextReference
	for _, ref := range session.Workspace.Attachments {
		if ref.Kind != "table" && ref.Kind != "project_view" {
			continue
		}
		if sourceID != "" && ref.SourceID != sourceID {
			continue
		}
		schema, relation := splitAttachedRelation(ref.ObjectID)
		if sameRelation(root.Schema, root.Relation, schema, relation) {
			continue
		}
		targets = append(targets, ref)
	}
	if len(targets) == 0 {
		return query, false, nil
	}
	current := RecordSet{Title: query.Title, DTQL: query.DTQL, Source: query.Source, Parameters: query.Parameters}
	applied := false
	for _, target := range targets {
		candidates, candidateErr := application.Candidates(ctx, current)
		if candidateErr != nil {
			return query, false, candidateErr
		}
		schema, relation := splitAttachedRelation(target.ObjectID)
		matching := make([]JoinCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			// A one-to-many edge changes the root row count, so only use it
			// when the request explicitly names that attached relation.
			if (candidate.Cardinality == "many-to-one" || (candidate.Cardinality == "one-to-many" && promptMentionsRelation(prompt, relation))) && sameRelation(schema, relation, candidate.Target.Schema, candidate.Target.Relation) {
				matching = append(matching, candidate)
			}
		}
		if len(matching) == 0 {
			continue
		}
		if parsed.Limit() > 0 || parsed.Offset() > 0 {
			preserving := matching[:0]
			for _, candidate := range matching {
				if candidate.Cardinality == "many-to-one" {
					preserving = append(preserving, candidate)
				}
			}
			if len(preserving) == 0 {
				return query, false, &attachedJoinChoiceError{question: fmt.Sprintf("Joining attached %s would multiply rows before the requested limit. Please ask for %s as the main table and include the current table, or remove the limit.", sanitizeTerminalText(target.Title), sanitizeTerminalText(target.Title))}
			}
			matching = preserving
		}
		chosen, chooseErr := chooseAttachedJoin(matching, prompt, target.Title)
		if chooseErr != nil {
			return query, false, chooseErr
		}
		joined, applyErr := application.ApplyAttached(ctx, current, chosen.ID)
		if applyErr != nil {
			return query, false, applyErr
		}
		current.Title, current.DTQL, current.Lineage = joined.Title, joined.DTQL, joined.Lineage
		query = joined
		applied = true
	}
	if !applied {
		return query, false, nil
	}
	// This is one fresh query, not a child of a persisted RecordSet.
	query.Lineage = nil
	query.SourceID = sourceID
	return query, true, nil
}

func promptMentionsRelation(prompt, relation string) bool {
	words := map[string]bool{}
	for _, word := range identifierWords(prompt) {
		words[word] = true
	}
	for _, word := range identifierWords(relation) {
		if words[word] || words[word+"s"] {
			continue
		}
		if strings.HasSuffix(word, "y") && words[strings.TrimSuffix(word, "y")+"ies"] {
			continue
		}
		return false
	}
	return len(identifierWords(relation)) > 0
}

func splitAttachedRelation(id string) (string, string) {
	if schema, relation, found := strings.Cut(id, "."); found {
		return schema, relation
	}
	return "", id
}

type attachedJoinChoiceError struct{ question string }

func (e *attachedJoinChoiceError) Error() string { return e.question }

func chooseAttachedJoin(candidates []JoinCandidate, prompt, target string) (JoinCandidate, error) {
	if len(candidates) == 0 {
		return JoinCandidate{}, fmt.Errorf("no readable foreign-key relationship to attached table %s was found", sanitizeTerminalText(target))
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	words := map[string]bool{}
	for _, word := range identifierWords(prompt) {
		words[word] = true
	}
	var selected []JoinCandidate
	for _, candidate := range candidates {
		unique := joinChoiceTokens(candidate)
		for _, other := range candidates {
			if other.ID == candidate.ID {
				continue
			}
			for word := range joinChoiceTokens(other) {
				delete(unique, word)
			}
		}
		for word := range unique {
			if words[word] {
				selected = append(selected, candidate)
				break
			}
		}
	}
	if len(selected) == 1 {
		return selected[0], nil
	}
	options := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		fields := make([]string, len(candidate.Fields))
		for i, pair := range candidate.Fields {
			fields[i] = sanitizeTerminalText(pair.SourceField)
		}
		options = append(options, strings.Join(fields, "+"))
	}
	return JoinCandidate{}, &attachedJoinChoiceError{question: fmt.Sprintf("Which relationship to attached table %s should I use: %s? Please name one in your next request.", sanitizeTerminalText(target), strings.Join(options, " or "))}
}
