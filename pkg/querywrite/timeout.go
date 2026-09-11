package querywrite

import "time"

// Timeout bounds one query write's wait on the project's query store: the
// revisioned store serializes writers with a cross-process lock, and a lock
// another process holds must end the request with 504 TIMEOUT, having
// written nothing, rather than hang it until the client gives up. It is
// shorter than `datatug serve`'s 10-second http.Server WriteTimeout, so
// the 504 still reaches the client.
const Timeout = 5 * time.Second
