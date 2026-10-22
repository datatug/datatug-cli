package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cardStoreStub struct {
	datatug.ProjectStore
	cards   map[string]*datatug.Card
	saveErr error
	listErr error
	delErr  error
	loadErr error
}

func (s *cardStoreStub) LoadCard(_ context.Context, id string, _ ...datatug.StoreOption) (*datatug.Card, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	if c, ok := s.cards[id]; ok {
		return c, nil
	}
	return nil, os.ErrNotExist
}
func (s *cardStoreStub) LoadCards(context.Context, ...datatug.StoreOption) (datatug.Cards, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out datatug.Cards
	for _, c := range s.cards {
		out = append(out, c)
	}
	return out, nil
}
func (s *cardStoreStub) SaveCard(_ context.Context, c *datatug.Card) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.cards[c.ID] = c
	return nil
}
func (s *cardStoreStub) DeleteCard(_ context.Context, id string) error {
	if s.delErr != nil {
		return s.delErr
	}
	delete(s.cards, id)
	return nil
}

func apiTestCard(id string) datatug.Card {
	return datatug.Card{ID: id, Title: "Customer", Widget: &datatug.BoardWidget{Name: "text", Data: &datatug.TextWidgetDef{Markdown: "x"}}}
}

func TestCardAPI(t *testing.T) {
	serveProjects(t, "p1")
	orig := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = orig }()
	ctx := context.Background()
	ref := dto.ProjectRef{ProjectID: "p1"}
	itemRef := dto.ProjectItemRef{ProjectRef: ref, ID: "c1"}

	// The store cannot be opened.
	storage.NewDatatugStore = func(string) (storage.Store, error) { return nil, errors.New("store err") }
	_, err := CreateCard(ctx, ref, apiTestCard("c1"))
	assert.Error(t, err)
	_, err = GetCard(ctx, itemRef)
	assert.Error(t, err)
	_, err = ListCards(ctx, ref)
	assert.Error(t, err)
	assert.Error(t, DeleteCard(ctx, itemRef))
	_, err = SaveCard(ctx, ref, apiTestCard("c1"))
	assert.Error(t, err)

	stub := &cardStoreStub{cards: map[string]*datatug.Card{}}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return stub }}, nil
	}

	// Create, then a second create of the same ID is refused and replaces nothing.
	created, err := CreateCard(ctx, ref, apiTestCard("c1"))
	require.NoError(t, err)
	assert.Equal(t, "c1", created.ID)
	_, err = CreateCard(ctx, ref, apiTestCard("c1"))
	assert.ErrorContains(t, err, "already exists")

	got, err := GetCard(ctx, itemRef)
	require.NoError(t, err)
	assert.Equal(t, "Customer", got.Title)
	_, err = GetCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "missing"})
	assert.ErrorContains(t, err, `card "missing" not found`)

	briefs, err := ListCards(ctx, ref)
	require.NoError(t, err)
	require.Len(t, briefs, 1)
	assert.Equal(t, "c1", briefs[0].ID)

	changed := apiTestCard("c1")
	changed.Title = "Changed"
	saved, err := SaveCard(ctx, ref, changed)
	require.NoError(t, err)
	assert.Equal(t, "Changed", saved.Title)

	require.NoError(t, DeleteCard(ctx, itemRef))
	assert.ErrorIs(t, DeleteCard(ctx, itemRef), ErrItemNotFound)

	// Invalid input is refused before the store is asked.
	bad := apiTestCard("c2")
	bad.Title = ""
	_, err = CreateCard(ctx, ref, bad)
	assert.Error(t, err)
	_, err = SaveCard(ctx, ref, bad)
	assert.Error(t, err)
	for _, id := range []string{"", "../x", "a/b"} {
		_, err = CreateCard(ctx, ref, apiTestCard(id))
		assert.Error(t, err, id)
		_, err = SaveCard(ctx, ref, apiTestCard(id))
		assert.Error(t, err, id)
		_, err = GetCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: id})
		assert.Error(t, err, id)
		assert.Error(t, DeleteCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: id}), id)
	}
	_, err = ListCards(ctx, dto.ProjectRef{ProjectID: "../p"})
	assert.Error(t, err)
	_, err = CreateCard(ctx, dto.ProjectRef{ProjectID: "../p"}, apiTestCard("c9"))
	assert.Error(t, err)

	// A card that cannot be read is neither replaced nor taken for absent.
	stub.cards["corrupt"] = &datatug.Card{ID: "corrupt"}
	stub.loadErr = errors.New("unexpected end of JSON input")
	_, err = CreateCard(ctx, ref, apiTestCard("corrupt"))
	assert.ErrorContains(t, err, `could not create card "corrupt"`)
	assert.Equal(t, "", stub.cards["corrupt"].Title, "the unreadable card was not overwritten")
	// The dalgo not-found error counts as absent.
	stub.loadErr = record.ErrRecordNotFound
	_, err = CreateCard(ctx, ref, apiTestCard("fresh"))
	assert.NoError(t, err)
	stub.loadErr = nil
	delete(stub.cards, "fresh")
	delete(stub.cards, "corrupt")

	// Store failures are answered without the store's own text.
	stub.saveErr = errors.New("disk /secret/path full")
	_, err = CreateCard(ctx, ref, apiTestCard("c3"))
	assert.ErrorContains(t, err, `could not create card "c3"`)
	assert.NotContains(t, err.Error(), "secret")
	_, err = SaveCard(ctx, ref, apiTestCard("c3"))
	assert.ErrorContains(t, err, `could not save card "c3"`)
	stub.saveErr = nil
	stub.cards["c4"] = &datatug.Card{ID: "c4"}
	stub.delErr = errors.New("disk /secret/path locked")
	err = DeleteCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "c4"})
	assert.ErrorContains(t, err, `could not delete card "c4"`)
	stub.listErr = errors.New("disk /secret/path unreadable")
	_, err = ListCards(ctx, ref)
	assert.ErrorContains(t, err, "cards of project")
	assert.NotContains(t, err.Error(), "secret")
}

func TestLocalProjectCardsReadSelectedBranch(t *testing.T) {
	const projectID = "qualified-read-cards"
	dir, git := setupTestGitProject(t, projectID)
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	const body = `{"title":"%s","parameters":[{"id":"customerId","type":"string","isRequired":true,"match":[{"column":"CustomerID"}]}],"widget":{"name":"text","data":{"markdown":"x"}}}`
	write("cards/folder/card.json", strings.Replace(body, "%s", "Folder", 1))
	write("cards/flat.card.json", strings.Replace(body, "%s", "Flat", 1))
	write("cards/both/card.json", strings.Replace(body, "%s", "BothFolder", 1))
	write("cards/both.card.json", strings.Replace(body, "%s", "BothFlat", 1))
	write("cards/empty-dir/readme.md", "not a card")
	git("add", "-A")
	git("commit", "-q", "-m", "cards")
	branch := git("symbolic-ref", "--short", "HEAD")
	configureBranchReadTest(t, projectID, dir)
	ctx := context.Background()
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}

	briefs, err := LocalProjectCards(ctx, ref, branch)
	require.NoError(t, err)
	var ids []string
	for _, b := range briefs {
		ids = append(ids, b.ID)
	}
	assert.Equal(t, []string{"both", "flat", "folder"}, ids)
	assert.True(t, briefs[0].Parameters[0].IsRequired)
	assert.Equal(t, datatug.CardMatchRule{Column: "CustomerID"}, briefs[0].Parameters[0].Match[0])

	card, err := LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "both"}, branch)
	require.NoError(t, err)
	assert.Equal(t, "BothFolder", card.Title, "the folder form wins")
	card, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "flat"}, branch)
	require.NoError(t, err)
	assert.Equal(t, "Flat", card.Title)
	assert.Equal(t, "flat", card.ID)

	_, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "nope"}, branch)
	assert.ErrorContains(t, err, `card "nope" not found`)
	_, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "bad id"}, branch)
	assert.Error(t, err)
	_, err = LocalProjectCards(ctx, ref, "")
	assert.Error(t, err)
	_, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "flat"}, "")
	assert.Error(t, err)

	// A stored id that disagrees with the path and unknown keys fail the single read,
	// but the list skips them and keeps the valid cards.
	write("cards/liar/card.json", `{"id":"other","title":"T"}`)
	_, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "liar"}, branch)
	assert.Error(t, err)
	write("cards/extra/card.json", `{"title":"T","unknown":1}`)
	_, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "extra"}, branch)
	assert.Error(t, err)
	write("cards/untitled/card.json", `{"title":"","widget":{"name":"text"}}`)
	write("cards/a.b/card.json", strings.Replace(body, "%s", "Dotted", 1))
	write("cards/stray.dot.card.json", strings.Replace(body, "%s", "Dotted flat", 1))
	_, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "a.b"}, branch)
	assert.Error(t, err, "a dotted id violates the card id pattern")
	briefs, err = LocalProjectCards(ctx, ref, branch)
	require.NoError(t, err)
	ids = nil
	for _, b := range briefs {
		ids = append(ids, b.ID)
	}
	assert.Equal(t, []string{"both", "flat", "folder"}, ids)
}

func TestLocalProjectCardsFailClosed(t *testing.T) {
	const projectID = "qualified-read-bad-cards"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	branch := git("symbolic-ref", "--short", "HEAD")
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}
	ctx := context.Background()

	// cards is a file, not a folder.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cards"), []byte("x"), 0o644))
	_, err := LocalProjectCards(ctx, ref, branch)
	assert.Error(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, "cards")))

	// A file name that is no valid ID is skipped, not fatal.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cards"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cards", "bad id.card.json"), []byte(`{}`), 0o644))
	briefs, err := LocalProjectCards(ctx, ref, branch)
	require.NoError(t, err)
	assert.Empty(t, briefs)
	require.NoError(t, os.Remove(filepath.Join(dir, "cards", "bad id.card.json")))

	// A file over the read bound is refused.
	big := `{"title":"` + strings.Repeat("x", maxQualifiedReadBytes+1) + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cards", "big.card.json"), []byte(big), 0o644))
	_, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "big"}, branch)
	assert.Error(t, err)
}

func TestReadQualifiedCardsTooMany(t *testing.T) {
	const projectID = "qualified-read-many-cards"
	dir, _ := setupTestGitProject(t, projectID)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cards"), 0o755))
	for i := 0; i <= maxQualifiedProjectItems; i++ {
		name := filepath.Join(dir, "cards", "c"+strings.Repeat("x", 1)+itoa(i)+".card.json")
		require.NoError(t, os.WriteFile(name, []byte(`{}`), 0o644))
	}
	reader, err := openQualifiedProjectReader(dir)
	require.NoError(t, err)
	defer reader.close()
	_, err = readQualifiedCards(reader)
	assert.ErrorContains(t, err, "exceeds")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for ; i > 0; i /= 10 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
	}
	return string(digits)
}
