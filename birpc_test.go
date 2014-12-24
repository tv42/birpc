package birpc_test

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/tv42/birpc"
	"github.com/tv42/birpc/jsonmsg"
)

// Generic reply parsing
type LowLevelReply struct {
	Id     uint64          `json:"id,string"`
	Result json.RawMessage `json:"result"`
	Error  *birpc.Error    `json:"error"`
}

type WordLengthRequest struct {
	Word string
}

type WordLengthReply struct {
	Length int
}

type WordLength_LowLevelReply struct {
	Id     uint64          `json:"id,string"`
	Result WordLengthReply `json:"result"`
	Error  *birpc.Error    `json:"error"`
}

type WordLength struct{}

func (_ WordLength) Len(request *WordLengthRequest, reply *WordLengthReply) error {
	reply.Length = len(request.Word)
	return nil
}

// this is here only to trigger a bug where all methods are thought to
// be rpc methods
func (_ WordLength) redHerring() {
}

func makeRegistry() *birpc.Registry {
	r := birpc.NewRegistry()
	r.RegisterService(WordLength{})
	return r
}

const PALINDROME = `{"id": "42", "fn": "WordLength.Len", "args": {"Word": "saippuakauppias"}}` + "\n"

func TestServerSimple(t *testing.T) {
	c, s := net.Pipe()
	defer c.Close()
	registry := makeRegistry()
	server := birpc.NewEndpoint(jsonmsg.NewCodec(s), registry)
	server_err := make(chan error)
	go func() {
		server_err <- server.Serve()
	}()

	io.WriteString(c, PALINDROME)

	var reply WordLength_LowLevelReply
	dec := json.NewDecoder(c)
	if err := dec.Decode(&reply); err != nil && err != io.EOF {
		t.Fatalf("decode failed: %s", err)
	}
	t.Logf("reply msg: %#v", reply)
	if reply.Error != nil {
		t.Fatalf("unexpected error response: %v", reply.Error)
	}
	if reply.Result.Length != 15 {
		t.Fatalf("got wrong answer: %v", reply.Result.Length)
	}

	c.Close()

	err := <-server_err
	if err != io.EOF {
		t.Fatalf("unexpected error from ServeCodec: %v", err)
	}
}

func TestClient(t *testing.T) {
	c, s := net.Pipe()
	defer c.Close()
	registry := makeRegistry()
	server := birpc.NewEndpoint(jsonmsg.NewCodec(s), registry)
	server_err := make(chan error)
	go func() {
		server_err <- server.Serve()
	}()

	client := birpc.NewEndpoint(jsonmsg.NewCodec(c), nil)
	client_err := make(chan error)
	go func() {
		client_err <- client.Serve()
	}()

	// Synchronous calls
	args := &WordLengthRequest{"xyzzy"}
	reply := &WordLengthReply{}
	err := client.Call("WordLength.Len", args, reply)
	if err != nil {
		t.Errorf("unexpected error from call: %v", err.Error())
	}
	if reply.Length != 5 {
		t.Fatalf("got wrong answer: %v", reply.Length)
	}

	c.Close()

	err = <-server_err
	if err != io.EOF {
		t.Fatalf("unexpected error from peer ServeCodec: %v", err)
	}

	err = <-client_err
	if err != io.ErrClosedPipe {
		t.Fatalf("unexpected error from local ServeCodec: %v", err)
	}
}

func TestClientNilResult(t *testing.T) {
	c, s := net.Pipe()
	defer c.Close()
	registry := makeRegistry()
	server := birpc.NewEndpoint(jsonmsg.NewCodec(s), registry)
	server_err := make(chan error)
	go func() {
		server_err <- server.Serve()
	}()

	client := birpc.NewEndpoint(jsonmsg.NewCodec(c), nil)
	client_err := make(chan error)
	go func() {
		client_err <- client.Serve()
	}()

	// Synchronous calls
	args := &WordLengthRequest{"xyzzy"}
	err := client.Call("WordLength.Len", args, nil)
	if err != nil {
		t.Errorf("unexpected error from call: %v", err.Error())
	}

	c.Close()

	err = <-server_err
	if err != io.EOF {
		t.Fatalf("unexpected error from peer ServeCodec: %v", err)
	}

	err = <-client_err
	if err != io.ErrClosedPipe {
		t.Fatalf("unexpected error from local ServeCodec: %v", err)
	}
}

type EndpointPeer struct {
	seen *birpc.Endpoint
}

type nothing struct{}

func (e *EndpointPeer) Poke(request *nothing, reply *nothing, endpoint *birpc.Endpoint) error {
	if e.seen != nil {
		panic("poke called twice")
	}
	e.seen = endpoint
	return nil
}

func TestServerEndpointArg(t *testing.T) {
	peer := &EndpointPeer{}
	registry := birpc.NewRegistry()
	registry.RegisterService(peer)

	c, s := net.Pipe()
	defer c.Close()

	server := birpc.NewEndpoint(jsonmsg.NewCodec(s), registry)
	server_err := make(chan error)
	go func() {
		server_err <- server.Serve()
	}()

	io.WriteString(c, `{"id":"42","fn":"EndpointPeer.Poke","args":{}}`)

	var reply LowLevelReply
	dec := json.NewDecoder(c)
	if err := dec.Decode(&reply); err != nil && err != io.EOF {
		t.Fatalf("decode failed: %s", err)
	}
	t.Logf("reply msg: %#v", reply)
	if reply.Error != nil {
		t.Fatalf("unexpected error response: %v", reply.Error)
	}
	c.Close()

	err := <-server_err
	if err != io.EOF {
		t.Fatalf("unexpected error from ServeCodec: %v", err)
	}

	if peer.seen == nil {
		t.Fatalf("peer never saw a birpc.Endpoint")
	}
}

type Failing struct{}

func (_ Failing) Fail(request *nothing, reply *nothing) error {
	return errors.New("intentional")
}

func TestServerError(t *testing.T) {
	c, s := net.Pipe()
	defer c.Close()
	registry := birpc.NewRegistry()
	registry.RegisterService(Failing{})
	server := birpc.NewEndpoint(jsonmsg.NewCodec(s), registry)
	server_err := make(chan error)
	go func() {
		server_err <- server.Serve()
	}()

	const REQ = `{"id": "42", "fn": "Failing.Fail", "args": {}}` + "\n"
	io.WriteString(c, REQ)

	var reply LowLevelReply
	dec := json.NewDecoder(c)
	if err := dec.Decode(&reply); err != nil && err != io.EOF {
		t.Fatalf("decode failed: %s", err)
	}
	t.Logf("reply msg: %#v", reply)
	if reply.Error == nil {
		t.Fatalf("expected an error")
	}
	if g, e := reply.Error.Msg, "intentional"; g != e {
		t.Fatalf("unexpected error response: %q != %q", g, e)
	}
	if reply.Result != nil {
		t.Fatalf("got unexpected result: %v", reply.Result)
	}

	c.Close()

	err := <-server_err
	if err != io.EOF {
		t.Fatalf("unexpected error from ServeCodec: %v", err)
	}
}
