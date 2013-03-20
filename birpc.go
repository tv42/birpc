// Bidirectional RPC with JSON messages.
//
// Uses net/rpc, is inspired by net/rpc/jsonrpc, but does more than
// either:
//
// - fully bidirectional: server can call RPCs on the client
// - incoming messages with seq 0 are "untagged" and will not
//   be responded to
//
// This allows one to do RPC over websockets without sacrifing what
// they are good for: sending immediate notifications.
//
// While this is intended for websockets, any io.ReadWriteCloser will
// do.

package birpc

import (
	"fmt"
	"io"
	"log"
	"net/rpc"
	"reflect"
	"sync"
)

type function struct {
	receiver reflect.Value
	method   reflect.Method
	args     reflect.Type
	reply    reflect.Type
}

type Registry struct {
	// protects services
	mu        sync.RWMutex
	functions map[string]*function
}

func getRPCMethodsOfType(object interface{}) []*function {
	var fns []*function

	type_ := reflect.TypeOf(object)

	for i := 0; i < type_.NumMethod(); i++ {
		method := type_.Method(i)

		// TODO verify more

		fn := &function{
			receiver: reflect.ValueOf(object),
			method:   method,
			args:     method.Type.In(1).Elem(),
			reply:    method.Type.In(2).Elem(),
		}
		fns = append(fns, fn)
	}

	return fns
}

func (r *Registry) RegisterService(object interface{}) {
	methods := getRPCMethodsOfType(object)
	if len(methods) == 0 {
		panic(fmt.Sprintf("birpc.RegisterService: type %T has no exported methods of suitable type", object))
	}

	serviceName := reflect.Indirect(reflect.ValueOf(object)).Type().Name()

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, fn := range methods {
		name := serviceName + "." + fn.method.Name
		r.functions[name] = fn
	}
}

func NewRegistry() *Registry {
	r := &Registry{}
	r.functions = make(map[string]*function)
	return r
}

type Codec interface {
	ReadMessage(*Message) error

	// WriteMessage may be called concurrently. Codecs need to
	// protect themselves.
	WriteMessage(*Message) error

	UnmarshalArgs(msg *Message, args interface{}) error
	UnmarshalResult(msg *Message, result interface{}) error

	io.Closer
}

type FillArgser interface {
	FillArgs([]reflect.Value) error
}

type Endpoint struct {
	codec Codec

	client struct {
		// protects seq and pending
		mutex   sync.Mutex
		seq     uint64
		pending map[uint64]*rpc.Call
	}

	server struct {
		registry *Registry
		running  sync.WaitGroup
	}
}

// Dummy registry with no functions registered.
var dummyRegistry = NewRegistry()

// Create a new endpoint that uses codec to talk to a peer. To
// actually process messages, call endpoint.Serve; this is done so you
// can capture errors. Registry can be nil to serve no callables from
// this peer.
func NewEndpoint(codec Codec, registry *Registry) *Endpoint {
	if registry == nil {
		registry = dummyRegistry
	}
	e := &Endpoint{}
	e.codec = codec
	e.server.registry = registry
	e.client.pending = make(map[uint64]*rpc.Call)
	return e
}

func (e *Endpoint) serve_request(msg *Message) error {
	e.server.registry.mu.RLock()
	fn := e.server.registry.functions[msg.Func]
	e.server.registry.mu.RUnlock()
	if fn == nil {
		msg.Error = &Error{Msg: "No such function."}
		msg.Func = ""
		msg.Args = nil
		msg.Result = nil
		err := e.send(msg)
		if err != nil {
			// well, we can't report the problem to the client...
			return err
		}
		return nil
	}

	e.server.running.Add(1)
	go func(fn *function, msg *Message) {
		defer e.server.running.Done()
		e.call(fn, msg)
	}(fn, msg)
	return nil
}

func (e *Endpoint) serve_response(msg *Message) error {
	e.client.mutex.Lock()
	call, found := e.client.pending[msg.ID]
	delete(e.client.pending, msg.ID)
	e.client.mutex.Unlock()

	if !found {
		return fmt.Errorf("Server responded with unknown seq %v", msg.ID)
	}

	if msg.Error == nil {
		err := e.codec.UnmarshalResult(msg, call.Reply)
		if err != nil {
			call.Error = fmt.Errorf("Unmarshaling result: %v", err)
		}
	} else {
		call.Error = rpc.ServerError(msg.Error.Msg)
	}

	// notify the caller, but never block
	select {
	case call.Done <- call:
	default:
	}

	return nil
}

func (e *Endpoint) Serve() error {
	defer e.codec.Close()
	defer e.server.running.Wait()
	for {
		var msg Message
		err := e.codec.ReadMessage(&msg)
		if err != nil {
			return err
		}

		if msg.Func != "" {
			err = e.serve_request(&msg)
		} else {
			err = e.serve_response(&msg)
		}
		if err != nil {
			return err
		}
	}
}

func (e *Endpoint) send(msg *Message) error {
	return e.codec.WriteMessage(msg)
}

func (e *Endpoint) fillArgs(arglist []reflect.Value) error {
	for i := 0; i < len(arglist); i++ {
		switch arglist[i].Interface().(type) {
		case *Endpoint:
			arglist[i] = reflect.ValueOf(e)
		}
	}
	return nil
}

func (e *Endpoint) call(fn *function, msg *Message) {
	args := reflect.New(fn.args)
	err := e.codec.UnmarshalArgs(msg, args.Interface())
	if err != nil {
		msg.Error = &Error{Msg: err.Error()}
		msg.Func = ""
		msg.Args = nil
		msg.Result = nil
		err = e.send(msg)
		if err != nil {
			// well, we can't report the problem to the client...
			e.codec.Close()
			return
		}
	}
	reply := reflect.New(fn.reply)

	num_args := fn.method.Type.NumIn()
	arglist := make([]reflect.Value, num_args, num_args)

	arglist[0] = fn.receiver
	arglist[1] = args
	arglist[2] = reply

	if num_args > 3 {
		for i := 3; i < num_args; i++ {
			arglist[i] = reflect.Zero(fn.method.Type.In(i))
		}
		// first fill what we can
		err = e.fillArgs(arglist[3:])
		if err != nil {
			msg.Error = &Error{Msg: err.Error()}
			msg.Func = ""
			msg.Args = nil
			msg.Result = nil
			err = e.send(msg)
			if err != nil {
				// well, we can't report the problem to the client...
				e.codec.Close()
				return
			}
		}

		// then codec fills what it can
		if filler, ok := e.codec.(FillArgser); ok {
			err = filler.FillArgs(arglist[3:])
			if err != nil {
				msg.Error = &Error{Msg: err.Error()}
				msg.Func = ""
				msg.Args = nil
				msg.Result = nil
				err = e.send(msg)
				if err != nil {
					// well, we can't report the problem to the client...
					e.codec.Close()
					return
				}
			}
		}
	}

	retval := fn.method.Func.Call(arglist)
	erri := retval[0].Interface()
	if erri != nil {
		err := erri.(error)
		msg.Error = &Error{Msg: err.Error()}
		msg.Func = ""
		msg.Args = nil
		msg.Result = nil
		err = e.send(msg)
		if err != nil {
			// well, we can't report the problem to the client...
			e.codec.Close()
			return
		}
	}

	msg.Error = nil
	msg.Func = ""
	msg.Args = nil
	msg.Result = reply.Interface()

	err = e.send(msg)
	if err != nil {
		// well, we can't report the problem to the client...
		e.codec.Close()
		return
	}
}

// See net/rpc Client.Go
func (e *Endpoint) Go(function string, args interface{}, reply interface{}, done chan *rpc.Call) *rpc.Call {
	call := &rpc.Call{}
	call.ServiceMethod = function
	call.Args = args
	call.Reply = reply
	if done == nil {
		done = make(chan *rpc.Call, 10)
	} else {
		if cap(done) == 0 {
			log.Panic("birpc: done channel is unbuffered")
		}
	}
	call.Done = done

	msg := &Message{
		Func: function,
		Args: args,
	}

	e.client.mutex.Lock()
	e.client.seq++
	msg.ID = e.client.seq
	e.client.pending[msg.ID] = call
	e.client.mutex.Unlock()

	// put sending in a goroutine so a malicious client that
	// refuses to read cannot ever make a .Go call block
	go e.send(msg)
	return call
}

// See net/rpc Client.Call
func (e *Endpoint) Call(function string, args interface{}, reply interface{}) error {
	call := <-e.Go(function, args, reply, make(chan *rpc.Call, 1)).Done
	return call.Error
}
