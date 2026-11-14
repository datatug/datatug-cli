// Package dbmirror copies a chosen part of a Firestore database into an
// inGitDB database laid out exactly as an inGitDB "mirror" defines it.
//
// Firestore has no schema, so nothing is introspected: the collection list,
// the path of every record file and the shape every record must have come
// from the inGitDB collection definitions of the mirror (the TARGET), where
// one definition stands for one Firestore collection path, at any depth.
//
// A mirror policy (format datatug-mirror-policy/v1), kept in the DataTug
// project, says which defined collections may be copied, which are excluded
// and which fields are masked. A collection the policy does not declare is
// never copied: it is listed in the report instead.
//
// The source is only read. The engine depends on the Source interface, whose
// methods only read; the one Firestore implementation (firestore_source.go)
// opens a client whose every request passes a filter that lets only the four
// read RPCs through (TestFirestoreClientRefusesWrites sends writes through it),
// calls no write method of the Google SDK (TestNoWriteCallInPackage checks the
// source text), and a run against a fake Firestore server that fails every
// write RPC proves none is sent (TestFirestoreSourceSendsNoWrite).
// Credentials are never read or stored by this package: the Google SDK reads
// Application Default Credentials from the operator's environment.
package dbmirror
