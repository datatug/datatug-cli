// Package crossverify checks that a database an engine loaded from a generated SQL script holds what the
// SQLite source holds: the same tables, columns, keys, indexes and every value.
//
// It is an internal package on purpose. It is the verifier of the CI journey of `datatug db copy --to
// sqlscript:` (scripts/check-journey-mysql.sh), not a command: its long-term home is an extension of
// `datatug compare` once compare's known defects are fixed, and until then no second public comparison
// command exists.
//
// The target is read through its Go driver, never through text conversions in the engine: integers as
// int64, floats as the 64 bits the engine returns, decimals as exact rationals, blobs as bytes. Only a
// date or time is compared as text, in the format the column was created with. The rules for each
// column kind are in canon.go. The verifier does not use the script writer's literal code, so a bug of the
// writer cannot hide itself.
//
// What it expects is the plan the exporter makes for the same SQLite file (faithful.PlanScript), not the
// warnings of the export: a renamed index is expected under its new name, and the indexes an engine
// creates itself for a foreign key are modelled and left out.
package crossverify
