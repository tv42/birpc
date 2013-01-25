package birpc_test

import (
	"encoding/json"
	"github.com/tv42/birpc"
	"github.com/tv42/birpc/jsonmsg"
	"io"
	"net"
	"testing"
)

type Request struct {
	Word string
}

type Reply struct {
	Length int
}

type LowLevelReply struct {
	Id     uint64       `json:"id,string"`
	Result Reply        `json:"result"`
	Error  *birpc.Error `json:"error"`
}

type WordLength struct{}

func (_ WordLength) Len(request *Request, reply *Reply) error {
	reply.Length = len(request.Word)
	return nil
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

	var reply LowLevelReply
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
	args := &Request{"xyzzy"}
	reply := &Reply{}
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
