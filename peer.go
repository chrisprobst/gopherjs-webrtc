package main

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/gopherjs/gopherjs/js"
)

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Peer //////////////////////////////////
////////////////////////////////////////////////////////////////////////

var (
	ErrPeerClosed           = errors.New("Peer closed")
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

type Peer struct {
	*js.Object
	DataChannel *WebConn

	SignalingState string `js:"signalingState"`

	// Channels
	iceCandidateChan chan interface{}
	closedChan       SignalChan

	// Live cycle
	closedMtx sync.Mutex
	closed    bool
}

func NewPeer(ctx context.Context) (peer *Peer, err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			peer = nil
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

	// Create peer
	peer = &Peer{
		Object:           peerConnectionObject,
		DataChannel:      dataChannel,
		iceCandidateChan: make(chan interface{}, defautMaxICECandidates),
		closedChan:       make(SignalChan),
	}

	////////////////////////////////////////////////////////////////////////
	//////////////////////////////// Link events ///////////////////////////
	////////////////////////////////////////////////////////////////////////

	go func() {
		// Wait for peer connection to close
		<-peer.Closed()

		// Panic on javascript errors
		defer func() {
			e := recover()
			if e == nil {
				return
			}
			if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
				log.Printf("Peer failed during closing due to error (%v)", jsErr)
			} else {
				panic(e)
			}
		}()

		// Finally close the underlying peer connection object
		if peer.SignalingState != "closed" {
			peer.Object.Call("close")
		}

		// Also close the data channel
		peer.DataChannel.Close()
	}()

	go func() {
		// Wait for data channel to close
		<-peer.DataChannel.Closed()

		// Also close the peer connection
		peer.Close()

		return
	}()

	go func() {
		defer peer.Close()
		<-ctx.Done()
	}()

	go func() {
		defer cancel()
		<-peer.Closed()
	}()

	peer.AddEventListener("datachannel", false, func(evt *js.Object) {
		log.Printf("Peer failed due to error (%v)", ErrDataChannelTriggered)
		peer.Close()
	})

	peer.AddEventListener("icecandidate", false, func(evt *js.Object) {
		iceCandidate := evt.Get("candidate")
		if iceCandidate == nil {
			return
		}

		select {
		case peer.iceCandidateChan <- iceCandidate:
		default:
		}
	})

	return
}

func (p *Peer) AddEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	p.Object.Call("addEventListener", typ, listener, useCapture)
}

func (p *Peer) RemoveEventListener(typ string, useCapture bool, listener func(*js.Object)) {
	p.Object.Call("removeEventListener", typ, listener, useCapture)
}

func (p *Peer) CreateOffer(ctx context.Context) (object interface{}, err error) {
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

	object, err = WaitForPromise(ctx, p.Object.Call("createOffer", adaptedConstraints))
	if err != nil {
		return nil, err
	}

	_, err = WaitForPromise(ctx, p.Object.Call("setLocalDescription", object))
	if err != nil {
		return nil, err
	}

	return
}

func (p *Peer) AcceptOfferAndCreateAnswer(ctx context.Context, offer interface{}) (object interface{}, err error) {
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
	_, err = WaitForPromise(ctx, p.Object.Call("setRemoteDescription", rtcSessionDescription))
	if err != nil {
		return nil, err
	}

	object, err = WaitForPromise(ctx, p.Object.Call("createAnswer", adaptedConstraints))
	if err != nil {
		return nil, err
	}

	_, err = WaitForPromise(ctx, p.Object.Call("setLocalDescription", object))
	if err != nil {
		return nil, err
	}

	return
}

func (p *Peer) AcceptAnswer(ctx context.Context, answer interface{}) (err error) {
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
	_, err = WaitForPromise(ctx, p.Object.Call("setRemoteDescription", rtcSessionDescription))

	return
}

func (p *Peer) AddICECandidate(ctx context.Context, iceCandidate interface{}) (err error) {
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
	_, err = WaitForPromise(ctx, p.Object.Call("addIceCandidate", rtcICECandidate))

	return
}

func (p *Peer) WaitForICECandidate(ctx context.Context) (interface{}, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case iceCandidateChan := <-p.iceCandidateChan:
		return iceCandidateChan, nil
	case <-p.Closed():
		return nil, ErrPeerClosed
	}
}

func (p *Peer) PullAndAddICECandidates(ctx context.Context, pullFunc func(context.Context) (interface{}, error)) error {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		<-p.Closed()
	}()

	for {
		iceCandidate, err := pullFunc(ctx)
		if err != nil {
			log.Printf("Peer failed to pull ice candidate due to error (%v)", err)
			return err
		}

		if err := p.AddICECandidate(ctx, iceCandidate); err != nil {
			log.Printf("Peer failed to pull ice candidate due to error (%v)", err)
			return err
		}
	}
}

func (p *Peer) Dial(ctx context.Context, signaller DialSignaller) error {
	offer, err := p.CreateOffer(ctx)
	if err != nil {
		return err
	}

	if err := signaller.PushOffer(ctx, offer); err != nil {
		return err
	}

	answer, err := signaller.PullAnswer(ctx)
	if err != nil {
		return err
	}

	if err := p.AcceptAnswer(ctx, answer); err != nil {
		return err
	}

	go func() {
		if err := p.PullAndAddICECandidates(ctx, signaller.RequestICECandidate); err != nil {
			p.Close()
		}
	}()

	select {
	case <-p.Closed():
		return ErrPeerClosed
	case <-p.Open():
		return nil
	}
}

func (p *Peer) Listen(ctx context.Context, signaller ListenSignaller) error {
	offer, err := signaller.PullOffer(ctx)
	if err != nil {
		return err
	}

	answer, err := p.AcceptOfferAndCreateAnswer(ctx, offer)
	if err != nil {
		return err
	}

	if err := signaller.PushAnswer(ctx, answer); err != nil {
		return err
	}

	go func() {
		if err := p.PullAndAddICECandidates(ctx, signaller.RequestICECandidate); err != nil {
			p.Close()
		}
	}()

	select {
	case <-p.Closed():
		return ErrPeerClosed
	case <-p.Open():
		return nil
	}
}

func (p *Peer) Open() SignalChan {
	return p.DataChannel.Open()
}

func (p *Peer) Closed() SignalChan {
	return p.closedChan
}

func (p *Peer) Close() error {
	p.closedMtx.Lock()

	if p.closed {
		p.closedMtx.Unlock()
		return nil
	}

	p.closed = true
	p.closedMtx.Unlock()

	close(p.closedChan)
	return nil
}
