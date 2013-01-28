package wetsock_test

import (
	"code.google.com/p/go.net/websocket"
	"fmt"
	"github.com/tv42/birpc"
	"github.com/tv42/birpc/oneshotlisten"
	"github.com/tv42/birpc/wetsock"
	"io"
	"log"
	"net"
	"net/http"
	"testing"
)

type Message struct {
	Greeting string
}

func hello(ws *websocket.Conn) {
	log.Printf("HELLO")
	codec := wetsock.NewCodec(ws)

	msg := birpc.Message{
		ID:   42,
		Func: "Greeting.Greet",
		Args: struct{ Msg string }{"Hello, world"},
	}
	err := codec.WriteMessage(&msg)
	if err != nil {
		panic(fmt.Sprintf("wetsock send failed: %v", err))
	}
	codec.Close()
}

type nothing struct{}

func TestSend(t *testing.T) {
	// just pipe would deadlock the server and client; we rely on
	// buffered io to be enough to allow them to work
	pipe_client, pipe_server := net.Pipe()

	server := http.Server{
		Handler: websocket.Handler(hello),
	}

	fakeListener := oneshotlisten.New(pipe_server)
	done := make(chan error)
	go func() {
		defer close(done)
		done <- server.Serve(fakeListener)
	}()

	conf, err := websocket.NewConfig("http://fakeserver.test/bloop", "http://fakeserver.test/blarg")
	if err != nil {
		t.Fatalf("websocket client config failed: %v", err)
	}
	ws, err := websocket.NewClient(conf, pipe_client)
	if err != nil {
		t.Fatalf("websocket client failed to start: %v", err)
	}
	var msg birpc.Message
	err = websocket.JSON.Receive(ws, &msg)
	if err != nil {
		t.Fatalf("websocket client receive error: %v", err)
	}
	if msg.ID != 42 {
		t.Errorf("unexpected seqno: %#v", msg)
	}
	if msg.Func != "Greeting.Greet" {
		t.Errorf("unexpected func: %#v", msg)
	}
	if msg.Args == nil {
		t.Errorf("unexpected args: %#v", msg)
	}
	if msg.Result != nil {
		t.Errorf("unexpected result: %#v", msg)
	}
	if msg.Error != nil {
		t.Errorf("unexpected error: %#v", msg)
	}

	switch greeting := msg.Args.(type) {
	case map[string]interface{}:
		if greeting["Msg"] != "Hello, world" {
			t.Errorf("unexpected greeting: %#v", greeting)
		}

	default:
		t.Fatalf("unexpected args type: %T: %v", msg.Args, msg.Args)
	}

	err = <-done
	if err != nil && err != io.EOF {
		t.Fatalf("http server failed: %v", err)
	}
}

type Address struct {
	Address string
}

type Peer struct{}

func (_ Peer) Address(request *nothing, reply *Address, ws *websocket.Conn) error {
	reply.Address = ws.Request().RemoteAddr
	return nil
}

func TestWSArg(t *testing.T) {
	registry := birpc.NewRegistry()
	registry.RegisterService(Peer{})

	pipe_client, pipe_server := net.Pipe()

	serve := func(ws *websocket.Conn) {
		endpoint := wetsock.NewEndpoint(registry, ws)
		err := endpoint.Serve()
		if err != nil {
			log.Printf("websocket error from %v: %v", ws.Request().RemoteAddr, err)
		}
	}
	server := http.Server{
		Handler: websocket.Handler(serve),
	}

	fakeListener := oneshotlisten.New(pipe_server)
	done := make(chan error)
	go func() {
		defer close(done)
		done <- server.Serve(fakeListener)
	}()

	conf, err := websocket.NewConfig("http://fakeserver.test/bloop", "http://fakeserver.test/blarg")
	if err != nil {
		t.Fatalf("websocket client config failed: %v", err)
	}
	ws, err := websocket.NewClient(conf, pipe_client)
	if err != nil {
		t.Fatalf("websocket client failed to start: %v", err)
	}
	request := birpc.Message{
		ID:   13,
		Func: "Peer.Address",
		Args: nothing{},
	}
	err = websocket.JSON.Send(ws, &request)
	if err != nil {
		t.Fatalf("websocket send failed: %v", err)
	}

	var msg birpc.Message
	err = websocket.JSON.Receive(ws, &msg)
	if err != nil {
		t.Fatalf("websocket client receive error: %v", err)
	}
	if msg.ID != 13 {
		t.Errorf("unexpected seqno: %#v", msg)
	}
	if msg.Func != "" {
		t.Errorf("unexpected func: %#v", msg)
	}
	if msg.Args != nil {
		t.Errorf("unexpected args: %#v", msg)
	}
	if msg.Result == nil {
		t.Errorf("unexpected result: %#v", msg)
	}
	if msg.Error != nil {
		t.Errorf("unexpected error: %#v", msg)
	}

	switch result := msg.Result.(type) {
	case map[string]interface{}:
		// this is what net.Pipe gives us
		if result["Address"] != "pipe" {
			t.Errorf("unexpected result: %#v", result)
		}

	default:
		t.Fatalf("unexpected result type: %T: %v", msg.Result, msg.Result)
	}

	err = <-done
	if err != nil && err != io.EOF {
		t.Fatalf("http server failed: %v", err)
	}
}
