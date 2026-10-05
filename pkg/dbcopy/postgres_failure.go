package dbcopy

import (
	"errors"
	"strings"

	"github.com/dal-go/dalgo2postgres"
)

// What a person reads when a PostgreSQL source cannot be reached, at the open and at any later call alike: the
// sentence the adapter chose for the failure (it is fixed by the kind of failure and the SQLSTATE of the server's
// answer, never by a driver's text), and the hint of where the connection string is read from. Never the connection
// string, the host, the port, the database, the user, the password, the text of a driver or a path of the machine.

// adapterTextPrefix starts the text of every error of the adapter.
const adapterTextPrefix = "dalgo2postgres: "

// adapterSteps are the steps of the open that the adapter names in front of its sentence; a call after the open names
// none.
var adapterSteps = []string{"sql.Open", "PingContext"}

// adapterSentence is the fixed sentence of err, the text the adapter writes for it without the parts it may name (the
// host, the port and the database: those are the person's own connection string, left out), without its prefix and
// without the step that failed. A SQLSTATE of the wrong shape is not shown (the adapter checks the shape; this does
// not rest on that). err itself is not changed.
func adapterSentence(err *dalgo2postgres.ConnectionError) string {
	bare := *err
	bare.Host, bare.Port, bare.Database = "", "", ""
	if !sqlStateShape.MatchString(bare.SQLState) {
		bare.SQLState = ""
	}
	text := strings.TrimPrefix(bare.Error(), adapterTextPrefix)
	for _, step := range adapterSteps {
		text = strings.TrimPrefix(text, step+": ")
	}
	return text
}

// connectionHintLead starts the hint.
const connectionHintLead = "the PostgreSQL connection string is read from "

// WithFlag returns the source with the name of the flag it was given on ("--db", "--from", "--to"), which the hint of
// a failure names when the connection string was typed there. The source it is called on is not changed.
func (r BackendRef) WithFlag(flag string) BackendRef {
	r.flag = flag
	return r
}

// connectionHint says where the connection string of this source is read from, by the name of the environment
// variable or of the flag and never by a value: the variable when the source was given as "env:NAME" (that is where
// the string is read from, whatever flag carried the name), else the flag, else only that it was given for the source.
func (r BackendRef) connectionHint() string {
	if name, isEnv := strings.CutPrefix(r.Raw, envPrefix); isEnv && ValidEnvName(name) {
		return connectionHintLead + "the environment variable " + name
	}
	if r.flag != "" {
		return connectionHintLead + "the " + r.flag + " flag"
	}
	return "the PostgreSQL connection string is the one the source was given"
}

// postgresFailureText is the one text of a PostgreSQL source that cannot be reached: the sentence and the hint.
func postgresFailureText(sentence, hint string) string { return sentence + "; " + hint }

// IsPostgresConnectionFailure reports whether err, or an error it wraps, is the failure of a PostgreSQL source that could
// not be opened or reached: the text OpenFailure gives for such a source, or the answer of a call that lost its
// connection (see guardPostgresError). It is how a command knows that it is to exit with the code of a connection that
// failed. A refusal of this package (the preview is off), the failure of a source that is not a PostgreSQL one and any
// other error are not.
func IsPostgresConnectionFailure(err error) bool {
	var (
		failed *openError
		lost   *postgresConnectionError
	)
	return (errors.As(err, &failed) && failed.postgres) || errors.As(err, &lost)
}
