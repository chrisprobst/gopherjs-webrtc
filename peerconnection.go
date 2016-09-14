package main

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/gopherjs/gopherjs/js"
)

////////////////////////////////////////////////////////////////////////
//////////////////////////////// PeerConnection ////////////////////////
////////////////////////////////////////////////////////////////////////

var (
	ErrPeerConnectionClosed = errors.New("PeerConnection is closed")
	ErrDataChannelTriggered = errors.New("DataChannel event triggered")

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
	dataChannelConfig = map[string]interface{}{
		"ordered":    true,
		"negotiated": true,
		"id":         1,
	}

	adaptedConstraints map[string]interface{}
)

////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////

const (
	defautMaxICECandidates = 100
)

////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////

func init() {
	if js.Global.Get("window").Get("webkitRTCPeerConnection") == js.Undefined {
		adaptedConstraints = constraints
	} else {
		adaptedConstraints = chromeConstraints
	}
}

////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////

type PeerConnection struct {
	*js.Object
	DataChannel *WebConn

	SignalingState string `js:"signalingState"`

	// Channels
	iceCandidateChan chan interface{}
	closedChan       chan struct{}

	// Live cycle
	closedMtx sync.Mutex
	closed    bool
}

func NewPeerConnection(ctx context.Context) (peerConnection *PeerConnection, err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			peerConnection = nil
			err = jsErr
		} else {
			panic(e)
		}
	}()

	// Derive new context
	ctx, cancel := context.WithCancel(ctx)

	// Create peer connection object
	peerConnectionObject := js.Global.Get("window").Get("RTCPeerConnection").New(peerConnectionConfig)

	// Create data channel
	dataChannel := NewWebConn(ctx, peerConnectionObject.Call("createDataChannel", "main", dataChannelConfig))

	// Create peer connection
	peerConnection = &PeerConnection{
		Object:           peerConnectionObject,
		DataChannel:      dataChannel,
		iceCandidateChan: make(chan interface{}, defautMaxICECandidates),
		closedChan:       make(chan struct{}),
	}

	////////////////////////////////////////////////////////////////////////
	//////////////////////////////// Link events ///////////////////////////
	////////////////////////////////////////////////////////////////////////

	go func() {
		// Wait for peer connection to close
		<-peerConnection.Closed()

		// Panic on javascript errors
		defer func() {
			e := recover()
			if e == nil {
				return
			}
			if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
				log.Printf("PeerConnection failed during closing due to error (%v)", jsErr)
			} else {
				panic(e)
			}
		}()

		// Finally close the underlying peer connection object
		if peerConnection.SignalingState != "closed" {
			peerConnection.Object.Call("close")
		}

		// Also close the data channel
		peerConnection.DataChannel.Close()
	}()

	go func() {
		// Wait for data channel to close
		<-peerConnection.DataChannel.Closed()

		// Also close the peer connection
		peerConnection.Close()

		return
	}()

	peerConnection.AddEventListener("datachannel", false, func(evt *js.Object) {
		log.Printf("PeerConnection failed due to error (%v)", ErrDataChannelTriggered)
		peerConnection.Close()
	})

	peerConnection.AddEventListener("icecandidate", false, func(evt *js.Object) {
		iceCandidate := evt.Get("candidate")
		if iceCandidate == nil {
			return
		}

		select {
		case peerConnection.iceCandidateChan <- iceCandidate:
		default:
		}
	})

	go func() {
		defer peerConnection.Close()
		<-ctx.Done()
	}()

	go func() {
		defer cancel()
		<-peerConnection.Closed()
	}()

	return
}

func (c *PeerConnection) AddEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	c.Object.Call("addEventListener", typ, listener, useCapture)
}

func (c *PeerConnection) RemoveEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	c.Object.Call("removeEventListener", typ, listener, useCapture)
}

func (c *PeerConnection) CreateOffer(ctx context.Context) (object interface{}, err error) {
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

	object, err = WaitForPromise(ctx, c.Object.Call("createOffer", adaptedConstraints))
	if err != nil {
		return nil, err
	}

	_, err = WaitForPromise(ctx, c.Object.Call("setLocalDescription", object))
	if err != nil {
		return nil, err
	}

	return
}

func (c *PeerConnection) AcceptOfferAndCreateAnswer(ctx context.Context, offer interface{}) (object interface{}, err error) {
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
	_, err = WaitForPromise(ctx, c.Object.Call("setRemoteDescription", rtcSessionDescription))
	if err != nil {
		return nil, err
	}

	object, err = WaitForPromise(ctx, c.Object.Call("createAnswer", adaptedConstraints))
	if err != nil {
		return nil, err
	}

	_, err = WaitForPromise(ctx, c.Object.Call("setLocalDescription", object))
	if err != nil {
		return nil, err
	}

	return
}

func (c *PeerConnection) AcceptAnswer(ctx context.Context, answer interface{}) (err error) {
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
	_, err = WaitForPromise(ctx, c.Object.Call("setRemoteDescription", rtcSessionDescription))

	return
}

func (c *PeerConnection) AddICECandidate(ctx context.Context, iceCandidate interface{}) (err error) {
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
	_, err = WaitForPromise(ctx, c.Object.Call("addIceCandidate", rtcICECandidate))

	return
}

func (c *PeerConnection) ICECandidates() <-chan interface{} {
	return c.iceCandidateChan
}

func (c *PeerConnection) Open() <-chan struct{} {
	return c.DataChannel.Open()
}

func (c *PeerConnection) Closed() <-chan struct{} {
	return c.closedChan
}

func (c *PeerConnection) Close() error {
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
