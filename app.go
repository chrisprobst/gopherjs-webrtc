package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/gopherjs/gopherjs/js"
)

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Util //////////////////////////////////
////////////////////////////////////////////////////////////////////////

type PromiseError struct {
	*js.Object
}

func (e *PromiseError) Error() string {
	return fmt.Sprintf("PromiseError: %s", e.Object.String())
}

func WaitForPromise(promise *js.Object) (*js.Object, error) {
	errChan := make(chan *js.Object, 1)
	resChan := make(chan *js.Object, 1)
	promise.Call("then", func(res *js.Object) { resChan <- res })
	promise.Call("catch", func(err *js.Object) { errChan <- err })
	select {
	case res := <-resChan:
		return res, nil
	case err := <-errChan:
		return nil, &PromiseError{err}
	}
}

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Config ////////////////////////////////
////////////////////////////////////////////////////////////////////////

var (
	peerConnectionConfig = map[string]interface{}{
		"iceServers": []interface{}{
			map[string]interface{}{
				"urls": "stun:chunkedswarm.com",
			},
			map[string]interface{}{
				"urls": "stun:stun.l.google.com:19302",
			},
		},
	}

	constraints = map[string]interface{}{
		"offerToReceiveAudio": false,
		"offerToReceiveVideo": false,
	}

	chromeConstraints = map[string]interface{}{
		"mandatory": map[string]interface{}{
			"OfferToReceiveAudio": false,
			"OfferToReceiveVideo": false,
		},
	}

	adaptedConstraints map[string]interface{}
)

func init() {
	if js.Global.Get("window").Get("webkitRTCPeerConnection") == js.Undefined {
		adaptedConstraints = constraints
	} else {
		adaptedConstraints = chromeConstraints
	}
}

////////////////////////////////////////////////////////////////////////
//////////////////////////////// DataChannel ///////////////////////////
////////////////////////////////////////////////////////////////////////

type ReadyState string

func (rs ReadyState) String() string {
	switch rs {
	case Connecting:
		return "Connecting"
	case Open:
		return "Open"
	case Closing:
		return "Closing"
	case Closed:
		return "Closed"
	default:
		return "Unknown"
	}
}

const (
	Connecting ReadyState = "connecting"
	Open       ReadyState = "open"
	Closing    ReadyState = "closing"
	Closed     ReadyState = "closed"
)

////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////

type DataChannel struct {
	*js.Object
	peerConnection *PeerConnection

	Label          string     `js:"label"`
	Ordered        bool       `js:"ordered"`
	Protocol       string     `js:"protocol"`
	ID             uint16     `js:"id"`
	ReadyState     ReadyState `js:"readyState"`
	BufferedAmount uint32     `js:"bufferedAmount"`
	Negotiated     bool       `js:"negotiated"`
	BinaryType     string     `js:"binaryType"`
}

func (c *DataChannel) AddEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	c.Object.Call("addEventListener", typ, listener, useCapture)
}

func (c *DataChannel) RemoveEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	c.Object.Call("removeEventListener", typ, listener, useCapture)
}

func (c *DataChannel) Send(data interface{}) (err error) {
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

func (c *DataChannel) Close() (err error) {
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

	c.Object.Call("close")

	return
}

////////////////////////////////////////////////////////////////////////
//////////////////////////////// PeerConnection ////////////////////////
////////////////////////////////////////////////////////////////////////

type PeerConnection struct {
	*js.Object
}

func NewPeerConnection() (pc *PeerConnection, err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			pc = nil
			err = jsErr
		} else {
			panic(e)
		}
	}()

	pc = &PeerConnection{
		js.Global.Get("window").Get("RTCPeerConnection").New(peerConnectionConfig),
	}

	return
}

func (c *PeerConnection) AddEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	c.Object.Call("addEventListener", typ, listener, useCapture)
}

func (c *PeerConnection) RemoveEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	c.Object.Call("removeEventListener", typ, listener, useCapture)
}

func (c *PeerConnection) CreateDataChannel() (dc *DataChannel, err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			dc = nil
			err = jsErr
		} else {
			panic(e)
		}
	}()

	dc = &DataChannel{
		Object: c.Object.Call("createDataChannel", "main", map[string]interface{}{
			"ordered":    true,
			"negotiated": true,
			"id":         1,
		}),
		peerConnection: c,
	}

	dc.BinaryType = "arraybuffer"

	return
}

func (c *PeerConnection) CreateOffer() (object *js.Object, err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			object = nil
			err = jsErr
		} else {
			panic(e)
		}
	}()

	object, err = WaitForPromise(c.Object.Call("createOffer", adaptedConstraints))
	if err != nil {
		return nil, err
	}

	_, err = WaitForPromise(c.Object.Call("setLocalDescription", object))
	if err != nil {
		return nil, err
	}

	return
}

func (c *PeerConnection) AcceptOfferAndCreateAnswer(offer *js.Object) (object *js.Object, err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			object = nil
			err = jsErr
		} else {
			panic(e)
		}
	}()

	rtcSessionDescription := js.Global.Get("window").Get("RTCSessionDescription").New(offer)
	_, err = WaitForPromise(c.Object.Call("setRemoteDescription", rtcSessionDescription))
	if err != nil {
		return nil, err
	}

	object, err = WaitForPromise(c.Object.Call("createAnswer", adaptedConstraints))
	if err != nil {
		return nil, err
	}

	_, err = WaitForPromise(c.Object.Call("setLocalDescription", object))
	if err != nil {
		return nil, err
	}

	return
}

func (c *PeerConnection) AcceptAnswer(answer *js.Object) (err error) {
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

	rtcSessionDescription := js.Global.Get("window").Get("RTCSessionDescription").New(answer)
	_, err = WaitForPromise(c.Object.Call("setRemoteDescription", rtcSessionDescription))

	return
}

func (c *PeerConnection) AddICECandidate(iceCandidate *js.Object) (err error) {
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

	rtcICECandidate := js.Global.Get("window").Get("RTCIceCandidate").New(iceCandidate)
	_, err = WaitForPromise(c.Object.Call("addIceCandidate", rtcICECandidate))

	return
}

func (c *PeerConnection) Close() (err error) {
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

	c.Object.Call("close")

	return
}

////////////////////////////////////////////////////////////////////////
//////////////////////////////// RTCConn ///////////////////////////////
////////////////////////////////////////////////////////////////////////

type RTCConn struct {
	peerConnection *PeerConnection
	dataChannel    *DataChannel
}

func DialRTC(signaller func(*js.Object) error) (c *RTCConn, err error) {
	peerConnection, err := NewPeerConnection()
	if err != nil {
		return nil, err
	}

	dataChannel, err := peerConnection.CreateDataChannel()
	if err != nil {
		return nil, err
	}

	dataChannel.AddEventListener("open", false, func(evt *js.Object) {
		log.Print("DataChannel is open")
	})

	peerConnection.AddEventListener("icecandidate", false, func(evt *js.Object) {
		iceCandidate := evt.Get("candidate")
		if iceCandidate == nil {
			return
		}

		go func() {
			if err := signaller(iceCandidate); err != nil {
				panic(err)
			}
		}()
	})

	return &RTCConn{
		peerConnection,
		dataChannel,
	}, nil
}

func (c *RTCConn) Write(b []byte) (int, error) {
	return 0, errors.New("Not implemented yet")
}

func (c *RTCConn) Read(b []byte) (int, error) {
	return 0, errors.New("Not implemented yet")
}

func (c *RTCConn) Close() error {
	return errors.New("Not implemented yet")
}

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Main //////////////////////////////////
////////////////////////////////////////////////////////////////////////

func main() {
	var (
		c1, c2 *RTCConn
		err    error
	)

	c1, err = DialRTC(func(iceCandidate *js.Object) error {
		return c2.peerConnection.AddICECandidate(iceCandidate)
	})
	if err != nil {
		panic(err)
	}

	c2, err = DialRTC(func(iceCandidate *js.Object) error {
		return c2.peerConnection.AddICECandidate(iceCandidate)
	})
	if err != nil {
		panic(err)
	}

	offer, err := c1.peerConnection.CreateOffer()
	if err != nil {
		panic(err)
	}

	answer, err := c2.peerConnection.AcceptOfferAndCreateAnswer(offer)
	if err != nil {
		panic(err)
	}

	err = c1.peerConnection.AcceptAnswer(answer)
	if err != nil {
		panic(err)
	}

	c2.dataChannel.AddEventListener("message", false, func(evt *js.Object) {
		log.Printf("DataChannel received message: %v", evt.Get("data"))
	})

	c1.dataChannel.AddEventListener("open", false, func(evt *js.Object) {
		go func() {
			b := make([]byte, 1024*8)
			_, err := io.ReadFull(rand.Reader, b)
			if err != nil {
				panic(err)
			}

			for i := 0; i < 10000; i++ {
				log.Print("Start sending...", c1.dataChannel.Send(b), c1.dataChannel.BufferedAmount)
			}
		}()
	})
}
