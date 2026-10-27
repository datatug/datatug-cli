package dbcopy

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2mongodb"
)

func parseMongoDB(raw, display string) (BackendRef, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" || strings.Contains(strings.TrimPrefix(u.Path, "/"), "/") {
		return BackendRef{}, errors.New("MongoDB source requires mongodb://host/database or mongodb+srv://host/database (use env:NAME for credentials)")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return BackendRef{Scheme: u.Scheme, Path: u.String(), Raw: display}, nil
}
func openMongoDB(ctx context.Context, r BackendRef, mode openMode) (dal.DB, error) {
	if mode.forWrite {
		return nil, errors.New("MongoDB sources are read-only; copying into MongoDB is unavailable")
	}
	return dalgo2mongodb.NewDB(ctx, r.Path)
}
