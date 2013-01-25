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

// Create a new endpoint that uses codec to talk to a peer. To
// actually process messages, call endpoint.Serve; this is done so you
// can capture errors.
func (r *Registry) NewEndpoint(codec Codec) *Endpoint {
	e := &Endpoint{}
	e.codec = codec
	e.server.registry = r
	return e
}

func NewRegistry() *Registry {
	r := &Registry{}
	r.functions = make(map[string]*function)
	return r
}

type Codec interface {
	ReadMessage(*Message) error
	WriteMessage(*Message) error

	UnmarshalArgs(msg *Message, args interface{}) error

	io.Closer
}

type Endpoint struct {
	codec   Codec
	sending sync.Mutex

	client struct {
		// protects seq
		mutex sync.Mutex
		seq   uint64
	}

	server struct {
		registry *Registry
	}
}

func (e *Endpoint) Serve() error {
	defer e.codec.Close()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		var msg Message
		err := e.codec.ReadMessage(&msg)
		if err != nil {
			return err
		}

		e.server.registry.mu.RLock()
		fn := e.server.registry.functions[msg.Func]
		e.server.registry.mu.RUnlock()
		if fn == nil {
			msg.Error = &Error{Msg: "No such function."}
			msg.Func = ""
			msg.Args = nil
			msg.Result = nil
			err = send(e.codec, &e.sending, &msg)
			if err != nil {
				// well, we can't report the problem to the client...
				return err
			}
			continue
		}

		wg.Add(1)
		go func(fn *function, codec Codec, sending *sync.Mutex, msg *Message) {
			defer wg.Done()
			call(fn, codec, sending, msg)
		}(fn, e.codec, &e.sending, &msg)
	}
}

func send(codec Codec, sending *sync.Mutex, msg *Message) error {
	sending.Lock()
	defer sending.Unlock()
	return codec.WriteMessage(msg)
}

func call(fn *function, codec Codec, sending *sync.Mutex, msg *Message) {
	args := reflect.New(fn.args)
	err := codec.UnmarshalArgs(msg, args.Interface())
	if err != nil {
		msg.Error = &Error{Msg: err.Error()}
		msg.Func = ""
		msg.Args = nil
		msg.Result = nil
		err = send(codec, sending, msg)
		if err != nil {
			// well, we can't report the problem to the client...
			codec.Close()
			return
		}
	}
	reply := reflect.New(fn.reply)

	retval := fn.method.Func.Call([]reflect.Value{fn.receiver, args, reply})
	erri := retval[0].Interface()
	if erri != nil {
		err := erri.(error)
		msg.Error = &Error{Msg: err.Error()}
		msg.Func = ""
		msg.Args = nil
		msg.Result = nil
		err = send(codec, sending, msg)
		if err != nil {
			// well, we can't report the problem to the client...
			codec.Close()
			return
		}
	}

	msg.Error = nil
	msg.Func = ""
	msg.Args = nil
	msg.Result = reply.Interface()

	err = send(codec, sending, msg)
	if err != nil {
		// well, we can't report the problem to the client...
		codec.Close()
		return
	}
}
