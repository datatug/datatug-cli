package dbmirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SourceIdentity names the database a mirror reads. It holds no credential:
// the resolved project and database ids, and the emulator host when
// FIRESTORE_EMULATOR_HOST was in effect (an emulator and a production project
// of one id are different sources).
type SourceIdentity struct {
	Project  string `json:"project"`
	Database string `json:"database"`
	Emulator string `json:"emulator,omitempty"`
}

func (s SourceIdentity) String() string {
	where := "Firestore project " + s.Project + ", database " + s.Database
	if s.Emulator != "" {
		where += ", on the emulator at " + s.Emulator
	}
	return where
}

// KeyEncoding turns a Firestore document id into the file name (and so the
// inGitDB record key) a mirror writes, and a file name back into the id.
//
// The encoding is tildeHexKeyEncoding ("~XX", ids.go; specified in
// docs/db-copy.md "Key encoding"). It is to be declared in the inGitDB schema
// and implemented once in ingitdb-go; until then the mirror implements it here,
// behind this interface, and records its name in the output's state
// (.datatug-mirror/output.json) instead of the collection definitions. A run
// refuses an output written with another encoding, so two are never mixed.
type KeyEncoding interface {
	// Name identifies the encoding in the output's state.
	Name() string
	// Encode is the file name of id.
	Encode(id string) string
	// Decode is the id of a file name, and false for a name Encode never writes.
	Decode(name string) (string, bool)
}

// tildeHexKeyEncoding is encodeID/decodeID: the mirror's own implementation
// until ingitdb-go provides the schema-declared one.
type tildeHexKeyEncoding struct{}

func (tildeHexKeyEncoding) Name() string                      { return "tilde-hex/v1" }
func (tildeHexKeyEncoding) Encode(id string) string           { return encodeID(id) }
func (tildeHexKeyEncoding) Decode(name string) (string, bool) { return decodeID(name) }

// keyEncoding is the encoding the mirror writes ids with.
var keyEncoding KeyEncoding = tildeHexKeyEncoding{}

const outputFormat = "datatug-mirror-output/v1"

func outputFile(out string) string { return filepath.Join(out, stateDirName, "output.json") }

// outputState is .datatug-mirror/output.json: what an output is a mirror of.
// It is written by the run that creates the output and never changed after.
type outputState struct {
	Format   string         `json:"format"`
	Source   SourceIdentity `json:"source"`
	Encoding string         `json:"id_encoding"`
}

// claimOutput makes sure the output is a mirror of this run's source, written
// with this version's key encoding, so no run (and no resume or prune) ever
// mixes two databases in one output, or deletes the records of one because
// another lacks them. An empty or missing folder is claimed for the source; a
// folder that is not empty and holds no output.json, or one of another source
// or encoding, is refused: choose an empty folder.
func (r *run) claimOutput() error {
	out := r.opts.Output
	want := outputState{Format: outputFormat, Source: r.opts.Identity, Encoding: keyEncoding.Name()}
	content, err := os.ReadFile(outputFile(out))
	if err == nil {
		var have outputState
		switch {
		case json.Unmarshal(content, &have) != nil || have.Format != outputFormat:
			return fmt.Errorf("%w: %s is not the output state of a db copy --schema=destination: choose an empty folder for the output", ErrUsage, outputFile(out))
		case have.Source != want.Source:
			return fmt.Errorf("%w: the output %s is a mirror of %s, not of %s: one output never mixes two databases, so choose an empty folder for this one", ErrUsage, out, have.Source, want.Source)
		case have.Encoding != want.Encoding:
			return fmt.Errorf("%w: the output %s names its record files by the key encoding %s, and this version uses %s: choose an empty folder", ErrUsage, out, have.Encoding, want.Encoding)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(out)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: the output %s is not empty and does not say which database it is a mirror of: choose an empty folder for the output", ErrUsage, out)
	}
	content, _ = json.MarshalIndent(want, "", "  ") // only strings
	_, err = writeFileIfChanged(outputFile(out), append(content, '\n'))
	return err
}
