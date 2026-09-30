// Package dtproject is the Projects module of the DataTug terminal UI: the list
// of local and GitHub projects, the screens of an open project, the wizard that
// creates a project and the flow that adds DataTug to an existing GitHub
// repository.
//
// Every screen is an Elm-style nav.Screen. Work that touches the disk, the
// network or the keyring is a tea.Cmd returning a result message, and anything
// that outlives one message (a clone, the GitHub device flow) is a stream: a
// command that starts the work and a command per progress message. Package
// variables in seams.go are the only doors to the outside world, so tests replace
// them instead of starting real processes.
package dtproject
