package endpoints

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
)

// maxExchangeBody bounds the body of a code exchange.
const maxExchangeBody = 1 << 10

// exchangeHandler is POST /datatug/session/exchange: the one route that needs no token. The body is
// {"code": "<one-time code from the link fragment>"}; the answer is {"token": "<session token>"}
// for a code that is valid, unused and unexpired (it is spent), and a fixed 401 {"error":
// "link-expired"} for any other (malformed, tampered, expired, used). Nothing the request sent is
// echoed or logged.
func exchangeHandler(access *agentaccess.Access) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeCORSOrigin(w, r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		var body struct {
			Code string `json:"code"`
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxExchangeBody))
		if err != nil || json.Unmarshal(raw, &body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad-request"}`))
			return
		}
		token, err := access.Exchange(body.Code)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"link-expired"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
	}
}
