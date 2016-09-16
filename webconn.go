package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/gopherjs/gopherjs/js"
)

////////////////////////////////////////////////////////////////////////
//////////////////////////////// WebConn ///////////////////////////////
////////////////////////////////////////////////////////////////////////

var (
	ErrWebConnClosed = errors.New("WebConn closed")
)

////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////

const (
	chunkSize     = 1024 * 16
	highWaterMark = 4 * 1024 * 1024
	lowWaterMark  = 128 * 1024
	pollTimeout   = time.Millisecond * 250
)

////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////

type WrappedReadyState struct {
	Index int
	Name  string
}

func (s *WrappedReadyState) String() string {
	return s.Name
}

var (
	Connecting = WrappedReadyState{0, "connecting"}
	Open       = WrappedReadyState{1, "open"}
	Closing    = WrappedReadyState{2, "closing"}
	Closed     = WrappedReadyState{3, "closed"}

	WrappedReadyStatesByIndex = map[int]WrappedReadyState{
		Connecting.Index: Connecting,
		Open.Index:       Open,
		Closing.Index:    Closing,
		Closed.Index:     Closed,
	}

	WrappedReadyStatesByName = map[string]WrappedReadyState{
		Connecting.Name: Connecting,
		Open.Name:       Open,
		Closing.Name:    Closing,
		Closed.Name:     Closed,
	}
)

////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////

type WebConn struct {
	*js.Object

	BufferedAmount uint32      `js:"bufferedAmount"`
	BinaryType     string      `js:"binaryType"`
	ReadyState     interface{} `js:"readyState"`

	// Channels
	openChan   SignalChan
	closedChan SignalChan

	// Live cycle
	closedMtx sync.Mutex
	closed    bool

	// Reading
	readBuffer bytes.Buffer
	readMtx    sync.Mutex
	readCond   *sync.Cond
}

func NewWebConn(ctx context.Context, object *js.Object) *WebConn {
	// Derive new context
	ctx, cancel := context.WithCancel(ctx)

	webConn := &WebConn{
		Object:     object,
		openChan:   make(SignalChan),
		closedChan: make(SignalChan),
	}
	webConn.readCond = sync.NewCond(&webConn.readMtx)
	webConn.BinaryType = "arraybuffer"

	if webConn.WrapReadyState() != Connecting {
		panic("WebConn object must be in connecting mode")
	}

	////////////////////////////////////////////////////////////////////////
	//////////////////////////////// Link events ///////////////////////////
	////////////////////////////////////////////////////////////////////////

	go func() {
		// Wait for web conn to close
		<-webConn.Closed()

		// Panic on javascript errors
		defer func() {
			e := recover()
			if e == nil {
				return
			}
			if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
				log.Printf("WebConn failed during closing due to error (%v)", jsErr)
			} else {
				panic(e)
			}
		}()

		// Finally close the underlying object
		webConn.Object.Call("close")

		log.Printf("WebConn closed")

		return
	}()

	go func() {
		defer webConn.Close()
		<-ctx.Done()
	}()

	go func() {
		defer cancel()
		<-webConn.Closed()
	}()

	webConn.AddEventListener("open", false, func(evt *js.Object) {
		log.Print("WebConn is open")
		close(webConn.openChan)
	})

	webConn.AddEventListener("error", false, func(evt *js.Object) {
		log.Printf("WebConn failed due to error (%v)", evt)
		webConn.Close()
	})

	webConn.AddEventListener("close", false, func(evt *js.Object) {
		webConn.Close()

		webConn.readMtx.Lock()
		defer webConn.readMtx.Unlock()

		readCond := webConn.readCond
		webConn.readCond = nil
		readCond.Broadcast()
	})

	webConn.AddEventListener("message", false, func(evt *js.Object) {
		data := js.Global.Get("Uint8Array").New(evt.Get("data")).Interface().([]byte)

		webConn.readMtx.Lock()
		defer webConn.readMtx.Unlock()

		webConn.readBuffer.Write(data)
		webConn.readCond.Broadcast()
	})

	return webConn
}

func (c *WebConn) AddEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	c.Object.Call("addEventListener", typ, listener, useCapture)
}

func (c *WebConn) RemoveEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	c.Object.Call("removeEventListener", typ, listener, useCapture)
}

func (c *WebConn) Send(data interface{}) (err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			err = jsErr
		} else {
			panic(e)
		}
	}()

	c.Object.Call("send", data)

	return
}

func (c *WebConn) Write(b []byte) (int, error) {
	totalSize := len(b)
	var chunk []byte
	for rem := totalSize; rem > 0; {
		size := rem
		if size > chunkSize {
			size = chunkSize
		}

		chunk, b = b[:size], b[size:]

		if err := c.Send(chunk); err != nil {
			return totalSize - rem, err
		}

		if c.BufferedAmount > highWaterMark {
			for c.BufferedAmount > lowWaterMark {
				time.Sleep(pollTimeout)
			}
		}

		rem -= size
	}

	return totalSize, nil
}

func (c *WebConn) Read(b []byte) (int, error) {
	c.readMtx.Lock()
	defer c.readMtx.Unlock()

	// Check if there is something to read
	for c.readBuffer.Len() == 0 {

		// Check if web conn is closed (if readCond is nil...)
		if c.readCond == nil {
			return 0, ErrWebConnClosed
		}

		// Wait for read condition
		c.readCond.Wait()
	}

	// Read from buffer and return result
	return c.readBuffer.Read(b)
}

func (c *WebConn) WrapReadyState() WrappedReadyState {
	readyState := c.ReadyState
	if readyStateIndex, ok := readyState.(int); ok {
		return WrappedReadyStatesByIndex[readyStateIndex]
	} else if readyStateName, ok := readyState.(string); ok {
		return WrappedReadyStatesByName[readyStateName]
	} else {
		panic("Unknown ready state")
	}
}

func (c *WebConn) Open() SignalChan {
	return c.openChan
}

func (c *WebConn) Closed() SignalChan {
	return c.closedChan
}

func (c *WebConn) Close() error {
	c.closedMtx.Lock()

	if c.closed {
		c.closedMtx.Unlock()
		return nil
	}

	c.closed = true
	c.closedMtx.Unlock()

	close(c.closedChan)
	return nil
}
