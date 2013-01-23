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

func makeServer() *birpc.Endpoint {
	s := birpc.New()
	s.RegisterService(WordLength{})
	return s
}

const PALINDROME = `{"id": "42", "fn": "WordLength.Len", "args": {"Word": "saippuakauppias"}}` + "\n"

func TestServerSimple(t *testing.T) {
	c, s := net.Pipe()
	defer c.Close()
	server := makeServer()
	ch := make(chan error)
	go func() {
		ch <- server.ServeCodec(jsonmsg.NewCodec(s))
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

	err := <-ch
	if err != io.EOF {
		t.Fatalf("unexpected error from ServeCodec: %v", err)
	}
}
