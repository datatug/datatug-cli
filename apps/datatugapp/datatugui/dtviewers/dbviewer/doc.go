// Package dbviewer is the DB viewer of the Viewers module: a chooser of database
// kinds, the SQLite and inGitDB home screens, and the browser of one database
// (tables and views, their columns, foreign keys, referrers and content).
//
// Every screen is a Bubble Tea model hosted by the tuigoff navigation shell:
// state lives in the model, loads are tea.Cmds that return result messages,
// components report with messages, and navigation is a message. Viewer and
// DbHomePage are the two entry points.
package dbviewer

import (
	// Registers the sqlite3 driver that dtviewers.GetSQLiteDbContext opens.
	_ "github.com/mattn/go-sqlite3"
)
