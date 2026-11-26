package sshdemo

import (
	"encoding/binary"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func ptyReqPayload(term string, w, h uint32) []byte {
	b := make([]byte, 4+len(term)+16+4)
	binary.BigEndian.PutUint32(b[0:4], uint32(len(term)))
	copy(b[4:], term)
	off := 4 + len(term)
	binary.BigEndian.PutUint32(b[off:], w)
	binary.BigEndian.PutUint32(b[off+4:], h)
	return b
}

func TestFilterSessionRequest(t *testing.T) {
	env := func(k string) []byte { return gossh.Marshal(struct{ Key, Value string }{k, "v"}) }
	win := make([]byte, 16)
	binary.BigEndian.PutUint32(win[0:4], 9999)
	binary.BigEndian.PutUint32(win[4:8], 9999)

	cases := []struct {
		name, typ string
		payload   []byte
		wantType  string
	}{
		{"pty-req kept and clamped", "pty-req", ptyReqPayload("xterm", 9999, 9999), "pty-req"},
		{"malformed pty-req refused", "pty-req", []byte{1}, refusedRequestType},
		{"window-change kept", "window-change", win, "window-change"},
		{"malformed window-change refused", "window-change", []byte{1}, refusedRequestType},
		{"shell kept", "shell", nil, "shell"},
		{"env TERM kept", "env", env("TERM"), "env"},
		{"env LD_PRELOAD refused", "env", env("LD_PRELOAD"), refusedRequestType},
		{"malformed env refused", "env", []byte{0xff}, refusedRequestType},
		{"exec refused", "exec", gossh.Marshal(struct{ Command string }{"id"}), refusedRequestType},
		{"subsystem refused", "subsystem", gossh.Marshal(struct{ Name string }{"sftp"}), refusedRequestType},
		{"agent forwarding refused", "auth-agent-req@openssh.com", nil, refusedRequestType},
		{"x11 refused", "x11-req", nil, refusedRequestType},
		{"signal refused", "signal", nil, refusedRequestType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var filter sessionRequestFilter
			got := filter.filterSessionRequest(&gossh.Request{Type: tc.typ, Payload: tc.payload})
			if got.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", got.Type, tc.wantType)
			}
			if tc.wantType == refusedRequestType && got.Payload != nil {
				t.Fatalf("refused request keeps payload %x", got.Payload)
			}
		})
	}
	var filter sessionRequestFilter
	clamped := filter.filterSessionRequest(&gossh.Request{Type: "pty-req", Payload: ptyReqPayload("xterm", 9999, 9999)})
	off := 4 + len("xterm")
	if w, h := binary.BigEndian.Uint32(clamped.Payload[off:]), binary.BigEndian.Uint32(clamped.Payload[off+4:]); w != MaxPTYWidth || h != MaxPTYHeight {
		t.Fatalf("pty-req clamped to %dx%d, want %dx%d", w, h, MaxPTYWidth, MaxPTYHeight)
	}
}

// Every request, refused or not, goes through the one ordered channel, so the
// session handler answers them in the order the client sent them.
func TestFilteringNewChannel_ForwardsEveryRequestInOrder(t *testing.T) {
	raw := make(chan *gossh.Request, 3)
	raw <- &gossh.Request{Type: "pty-req", Payload: ptyReqPayload("xterm", 80, 24)}
	raw <- &gossh.Request{Type: "exec", Payload: gossh.Marshal(struct{ Command string }{"id"})}
	raw <- &gossh.Request{Type: "shell"}
	close(raw)
	f := &filteringNewChannel{
		NewChannel: fakeNewChannel{reqs: raw},
		meta:       &sessionMeta{activityCh: make(chan struct{}, 1), chanDone: make(chan struct{})},
	}
	_, reqs, err := f.Accept()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for r := range reqs {
		got = append(got, r.Type)
	}
	want := []string{"pty-req", refusedRequestType, "shell"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

type fakeNewChannel struct {
	gossh.NewChannel
	reqs chan *gossh.Request
}

func (f fakeNewChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	return nil, f.reqs, nil
}
