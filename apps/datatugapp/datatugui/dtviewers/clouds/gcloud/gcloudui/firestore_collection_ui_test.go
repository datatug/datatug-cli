package gcloudui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"cloud.google.com/go/firestore"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
	"google.golang.org/api/iterator"
)

// fakeIterator yields canned results, then iterator.Done.
type fakeIterator struct {
	snaps []*firestore.DocumentSnapshot
	err   error
}

func (f *fakeIterator) Next() (*firestore.DocumentSnapshot, error) {
	if len(f.snaps) > 0 {
		snap := f.snaps[0]
		f.snaps = f.snaps[1:]
		return snap, nil
	}
	if f.err != nil {
		return nil, f.err
	}
	return nil, iterator.Done
}

func snapshot(id string) *firestore.DocumentSnapshot {
	return &firestore.DocumentSnapshot{Ref: &firestore.DocumentRef{ID: id}}
}

// stubDocs makes loadDocs read canned documents without a client.
func stubDocs(t *testing.T, iter documentIterator, clientErr error) {
	t.Helper()
	oldClient, oldOpen := newFirestoreClientFunc, openDocuments
	t.Cleanup(func() { newFirestoreClientFunc, openDocuments = oldClient, oldOpen })
	newFirestoreClientFunc = func(context.Context, string) (*firestore.Client, error) { return nil, clientErr }
	openDocuments = func(context.Context, *firestore.Client, string) documentIterator { return iter }
}

func docsScreen() collection {
	return newCollection(projectCtx(nil), &schemers.Collection{ID: "users"})
}

func TestLoadDocs(t *testing.T) {
	stubDocs(t, &fakeIterator{snaps: []*firestore.DocumentSnapshot{snapshot("a"), snapshot("b")}}, nil)
	msg := loadDocs("alpha-1", "users")().(docsLoaded)
	require.NoError(t, msg.err)
	require.Len(t, msg.docs, 2)
	assert.Equal(t, "b", msg.docs[1].id)

	stubDocs(t, &fakeIterator{}, errors.New("no client"))
	msg = loadDocs("alpha-1", "users")().(docsLoaded)
	assert.EqualError(t, msg.err, "no client")
	assert.True(t, msg.connect)

	stubDocs(t, &fakeIterator{err: errors.New("read failed")}, nil)
	msg = loadDocs("alpha-1", "users")().(docsLoaded)
	assert.EqualError(t, msg.err, "read failed")
	assert.False(t, msg.connect)
}

func TestCollection_ShowsDocuments(t *testing.T) {
	s := docsScreen()
	assert.Equal(t, "Collection: users — alpha-1", s.Title())
	assert.NotNil(t, s.Init())

	stubDocs(t, &fakeIterator{snaps: []*firestore.DocumentSnapshot{snapshot("doc-1"), snapshot("doc-2")}}, nil)
	h := harness(t, s)
	h.RequireContains("doc-1").RequireContains("doc-2").RequireContains("#")
	h.Press("down", "down")
	h.Resize(120, 40)
	h.RequireContains("doc-2")
}

func TestCollection_Messages(t *testing.T) {
	s := docsScreen()
	view := func(sc interface{ View() string }) string { return uitest.Plain(sc.View()) }

	pre, _ := s.Update(nav.ScreenFocusMsg{Focused: true}) // remembered until the grid exists
	s1, _ := pre.Update(tea.WindowSizeMsg{Width: 60, Height: 6})
	assert.Contains(t, view(s1), "Loading...")
	assert.True(t, s1.(collection).AtEdge(widgets.Up))
	assert.False(t, s1.(collection).Editing())
	_, cmd := s1.Update(uitest.Key("down")) // no grid yet
	assert.Nil(t, cmd)

	empty, cmd := s1.Update(docsLoaded{})
	assert.Nil(t, cmd)
	assert.Contains(t, view(empty), "No documents")

	connect, cmd := s1.Update(docsLoaded{err: errors.New("denied"), connect: true})
	assert.NotNil(t, cmd)
	assert.Contains(t, view(connect), "denied")
	assert.Contains(t, view(connect), "re-login or check scopes")

	read, cmd := s1.Update(docsLoaded{err: errors.New("broken")})
	assert.NotNil(t, cmd)
	assert.Contains(t, view(read), "broken")
	assert.NotContains(t, view(read), "re-login")

	docs := []firestoreDocRow{
		{id: "d1", data: map[string]any{"name": "Ann", "age": 30}},
		{id: "d2", data: map[string]any{"name": "Bob", "city": "Rome"}},
	}
	loaded, _ := s1.Update(docsLoaded{docs: docs})
	got := view(loaded)
	for _, want := range []string{"#", "age", "city", "name", "d1", "Ann", "30", "Rome"} {
		assert.Contains(t, got, want)
	}
	assert.True(t, loaded.(collection).grid.Focused())
	assert.False(t, loaded.(collection).Editing())
	assert.True(t, loaded.(collection).AtEdge(widgets.Up))
	moved, _ := loaded.Update(uitest.Key("down"))
	assert.False(t, moved.(collection).AtEdge(widgets.Up))
	resized, _ := moved.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	focused, _ := resized.Update(nav.ScreenFocusMsg{Focused: true})
	assert.True(t, focused.(collection).grid.Focused())
}
