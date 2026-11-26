// Package sshdemo is the supervisor of the public SSH demo of the DataTug
// terminal workspace (`datatug demo ssh-server`): a Charmbracelet Wish server
// that serves only interactive PTY sessions and runs each one as a separate
// child process of the same executable (`datatug demo ssh-session`) under its
// own slot uid, with per-session workspace, limits and cleanup. It also holds
// the edge forwarder and allow-listing egress proxy (`datatug demo ssh-edge`).
//
// Design: spec/features/ssh-demo/README.md. Nothing here renders a UI or
// knows about DataTug projects beyond preparing the read-only template.
package sshdemo
