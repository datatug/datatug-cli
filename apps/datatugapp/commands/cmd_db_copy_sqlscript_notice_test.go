package commands

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noticeQuery(path string) string { return "&notice-file=" + url.QueryEscape(path) }

func writeNotice(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "NOTICE.txt")
	require.NoError(t, os.WriteFile(p, content, 0o644))
	return p
}

func TestSQLScriptNoticeFileIsWrittenLineByLineInBothDialects(t *testing.T) {
	src := scriptSource(t)
	notice := writeNotice(t, []byte("\xef\xbb\xbfMIT License\r\n\r\nCopyright (c) 2024 Example\r\nGO\r\n\\! cmd\r\n*/\r\n"))
	for _, dialect := range []string{"mysql", "mssql"} {
		out := filepath.Join(t.TempDir(), "out.sql")
		_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", "sqlscript://"+out+"?dialect="+dialect+"&license=MIT"+noticeQuery(notice))
		require.NoError(t, err, dialect)
		b, err := os.ReadFile(out)
		require.NoError(t, err)
		script := strings.TrimPrefix(string(b), "\xef\xbb\xbf")
		want := "-- Licence: MIT\n--\n-- MIT License\n--\n-- Copyright (c) 2024 Example\n-- GO\n-- \\! cmd\n-- * /\n--\n"
		assert.Contains(t, script, want, dialect)
		assert.NotContains(t, script, "\r", dialect)
		// two runs are byte for byte the same
		out2 := filepath.Join(t.TempDir(), "again.sql")
		_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", "sqlscript://"+out2+"?dialect="+dialect+"&license=MIT"+noticeQuery(notice))
		require.NoError(t, err)
		b2, _ := os.ReadFile(out2)
		assert.Equal(t, b, b2, dialect)
	}
}

func TestSQLScriptWithoutANoticeIsUnchanged(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "out.sql")
	_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, "&license=MIT&attribution=X"))
	require.NoError(t, err)
	b, _ := os.ReadFile(out)
	assert.Contains(t, string(b), "-- Licence: MIT\n-- Attribution: X\n--\n-- Requires MySQL")
}

func TestSQLScriptNoticeFileIsRefusedAsAUsageError(t *testing.T) {
	src := scriptSource(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out", "out.sql")
	big := bytes.Repeat([]byte("a"), faithful.MaxNoticeBytes+1)
	cases := map[string]struct{ query, want string }{
		"missing":       {noticeQuery(filepath.Join(dir, "none.txt")), "notice-file:"},
		"empty value":   {"&notice-file=", "notice-file is empty"},
		"a folder":      {noticeQuery(dir), "not a regular file"},
		"nul":           {noticeQuery(writeNotice(t, []byte("a\x00b"))), "NUL"},
		"invalid utf-8": {noticeQuery(writeNotice(t, []byte("a\xffb"))), "not valid UTF-8"},
		"too large":     {noticeQuery(writeNotice(t, big)), "larger than 16384 bytes"},
		"blank":         {noticeQuery(writeNotice(t, []byte("\xef\xbb\xbf \r\n\t\n"))), "is empty"},
		"twice":         {noticeQuery(writeNotice(t, []byte("a"))) + noticeQuery(writeNotice(t, []byte("b"))), "times; give it once"},
		"semicolon":     {"&notice-file=/a;b", "%3B"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, c.query), "--json")
			require.Error(t, err)
			assert.Equal(t, 2, exitCodeOf(t, err))
			assert.Contains(t, err.Error(), c.want)
			assert.Contains(t, stdout.String(), `"status": "failed"`)
			_, statErr := os.Stat(filepath.Dir(out))
			assert.True(t, os.IsNotExist(statErr), "nothing is written")
		})
	}
}

func TestSQLScriptNoticeFileLimitsAreExact(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "out.sql")
	// exactly the limit is accepted, with or without a byte order mark in front
	for _, content := range [][]byte{bytes.Repeat([]byte("a"), faithful.MaxNoticeBytes), append([]byte("\xef\xbb\xbf"), bytes.Repeat([]byte("a"), faithful.MaxNoticeBytes)...)} {
		path := writeNotice(t, content)
		got, err := readNoticeFile(path)
		require.NoError(t, err)
		assert.Len(t, got, faithful.MaxNoticeBytes, "the notice is read whole")
		_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, noticeQuery(path)), "--overwrite", "recreate")
		require.NoError(t, err)
	}
}

func TestSQLScriptNoticeFileFailsToRead(t *testing.T) {
	orig := readNoticeBytes
	defer func() { readNoticeBytes = orig }()
	readNoticeBytes = func(io.Reader, int64) ([]byte, error) { return nil, errors.New("disk on fire") }
	_, err := readNoticeFile(writeNotice(t, []byte("a")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk on fire")
}

func TestSQLScriptNoticeFileThatCannotBeOpened(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens anything")
	}
	p := writeNotice(t, []byte("a"))
	require.NoError(t, os.Chmod(p, 0o000))
	_, err := readNoticeFile(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notice-file:")
}

func TestSQLScriptNoticeFileIsAcceptedRelative(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "N.txt"), []byte("Relative notice"), 0o644))
	t.Chdir(dir)
	s, err := readNoticeFile("N.txt")
	require.NoError(t, err)
	assert.Equal(t, "Relative notice", s)
}

func TestHostileNoticeIsAcceptedAndWrittenAsComments(t *testing.T) {
	s, err := readNoticeFile(writeNotice(t, []byte("\xef\xbb\xbfGO\r\n\\! cmd\r\nsource x\r\n/\r\n*/\u2028!! cmd\u2029\n")))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(s, "GO"), "one leading byte order mark was dropped")
	assert.Equal(t, []string{"-- GO", "-- \\! cmd", "-- source x", "-- /", "-- * /", "-- !! cmd"}, faithful.NoticeLines("-- ", s, nil))
}
