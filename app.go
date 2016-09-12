package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

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

func WaitForPromise(ctx context.Context, promise *js.Object) (*js.Object, error) {
	errChan := make(chan *js.Object, 1)
	resChan := make(chan *js.Object, 1)
	promise.Call("then", func(res *js.Object) { resChan <- res })
	promise.Call("catch", func(err *js.Object) { errChan <- err })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
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

	ErrPeerConnectionClosed = errors.New("PeerConnection is closed")
	ErrDataChannelClosed    = errors.New("DataChannel is closed")
	ErrDataChannelTriggered = errors.New("DataChannel event triggered")
)

const (
	chunkSize     = 1024 * 16
	highWaterMark = 4 * 1024 * 1024
	lowWaterMark  = 128 * 1024
	pollTimeout   = time.Millisecond * 250
	dataChannelID = 1
)

func init() {
	if js.Global.Get("window").Get("webkitRTCPeerConnection") == js.Undefined {
		adaptedConstraints = constraints
	} else {
		adaptedConstraints = chromeConstraints
	}
}

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Signaller /////////////////////////////
////////////////////////////////////////////////////////////////////////

type Signaller interface {
	PushOffer(context.Context, interface{}) error
	PullOffer(context.Context) (interface{}, error)
	PushAnswer(context.Context, interface{}) error
	PullAnswer(context.Context) (interface{}, error)
	PushICECandidate(context.Context, interface{}) error
	PullICECandidate(context.Context) (interface{}, error)
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

	Label          string     `js:"label"`
	Ordered        bool       `js:"ordered"`
	Protocol       string     `js:"protocol"`
	ID             uint16     `js:"id"`
	ReadyState     ReadyState `js:"readyState"`
	BufferedAmount uint32     `js:"bufferedAmount"`
	Negotiated     bool       `js:"negotiated"`
	BinaryType     string     `js:"binaryType"`

	// Channels
	closedChan chan struct{}

	// Live cycle
	closedMtx sync.Mutex
	closed    bool

	// Reading
	readBuffer bytes.Buffer
	readMtx    sync.Mutex
	readCond   *sync.Cond
}

func newDataChannel(dataChannelObject *js.Object) *DataChannel {
	dataChannel := &DataChannel{
		Object:     dataChannelObject,
		closedChan: make(chan struct{}),
	}
	dataChannel.readCond = sync.NewCond(&dataChannel.readMtx)
	dataChannel.BinaryType = "arraybuffer"

	////////////////////////////////////////////////////////////////////////
	//////////////////////////////// Link events ///////////////////////////
	////////////////////////////////////////////////////////////////////////

	go func() {
		// Wait for data channel to close
		<-dataChannel.Closed()

		// Make sure to wake up waiting readers
		dataChannel.readMtx.Lock()
		dataChannel.readCond.Broadcast()
		dataChannel.readCond = nil
		dataChannel.readMtx.Unlock()
	}()

	go func() {
		// Wait for data channel to close
		<-dataChannel.Closed()

		// Panic on javascript errors
		defer func() {
			e := recover()
			if e == nil {
				return
			}
			if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
				log.Printf("DataChannel failed during closing due to error (%v)", jsErr)
			} else {
				panic(e)
			}
		}()

		// Finally close the underlying data channel object
		dataChannel.Object.Call("close")

		return
	}()

	dataChannel.AddEventListener("open", false, func(evt *js.Object) {
		log.Print("DataChannel is open")
	})

	dataChannel.AddEventListener("error", false, func(evt *js.Object) {
		log.Printf("DataChannel failed due to error (%v)", evt)
		dataChannel.Close()
	})

	dataChannel.AddEventListener("close", false, func(evt *js.Object) {
		dataChannel.Close()
	})

	dataChannel.AddEventListener("message", false, func(evt *js.Object) {
		data := js.Global.Get("Uint8Array").New(evt.Get("data")).Interface().([]byte)

		dataChannel.readMtx.Lock()
		defer dataChannel.readMtx.Unlock()
		dataChannel.readBuffer.Write(data)
		dataChannel.readCond.Broadcast()
	})

	return dataChannel
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

func (c *DataChannel) Write(b []byte) (int, error) {
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

func (c *DataChannel) Read(b []byte) (int, error) {
	c.readMtx.Lock()
	defer c.readMtx.Unlock()

	// Check if there is something to read
	for c.readBuffer.Len() == 0 {

		// Check if data channel is closed (if readCond is nil...)
		if c.readCond == nil {
			return 0, ErrDataChannelClosed
		}

		// Wait for read condition
		c.readCond.Wait()
	}

	// Read from buffer and return result
	return c.readBuffer.Read(b)
}

func (c *DataChannel) Closed() <-chan struct{} {
	return c.closedChan
}

func (c *DataChannel) Close() error {
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

////////////////////////////////////////////////////////////////////////
//////////////////////////////// PeerConnection ////////////////////////
////////////////////////////////////////////////////////////////////////

type PeerConnection struct {
	*js.Object
	DataChannel *DataChannel

	SignalingState string `js:"signalingState"`

	// Channels
	closedChan chan struct{}

	// Live cycle
	closedMtx sync.Mutex
	closed    bool
}

func newPeerConnection(ctx context.Context, dial bool, signaller Signaller) (peerConnection *PeerConnection, err error) {
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
	dataChannel := newDataChannel(peerConnectionObject.Call("createDataChannel", "main", map[string]interface{}{
		"ordered":    true,
		"negotiated": true,
		"id":         dataChannelID,
	}))

	// Create peer connection
	peerConnection = &PeerConnection{
		Object:      peerConnectionObject,
		DataChannel: dataChannel,
		closedChan:  make(chan struct{}),
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

	go func() {
		defer peerConnection.Close()
		<-ctx.Done()
	}()

	go func() {
		defer cancel()
		<-peerConnection.Closed()
	}()

	////////////////////////////////////////////////////////////////////////
	//////////////////////////////// Dialing ///////////////////////////////
	////////////////////////////////////////////////////////////////////////

	// Close if data channel is open
	openChan := make(chan struct{})
	dataChannel.AddEventListener("open", false, func(evt *js.Object) {
		close(openChan)
	})

	// Listen for ice candidate and push to remote
	peerConnection.AddEventListener("icecandidate", false, func(evt *js.Object) {
		iceCandidate := evt.Get("candidate")
		if iceCandidate == nil {
			return
		}

		go func() {
			if err := signaller.PushICECandidate(ctx, iceCandidate); err != nil {
				log.Printf("PeerConnection failed to push ice candidate due to error (%v)", err)
				peerConnection.Close()
			}
		}()
	})

	// Dial or listen
	if dial {
		offer, err := peerConnection.CreateOffer(ctx)
		if err != nil {
			return nil, err
		}

		if err := signaller.PushOffer(ctx, offer); err != nil {
			return nil, err
		}

		answer, err := signaller.PullAnswer(ctx)
		if err != nil {
			return nil, err
		}

		if err := peerConnection.AcceptAnswer(ctx, answer); err != nil {
			return nil, err
		}
	} else {
		offer, err := signaller.PullOffer(ctx)
		if err != nil {
			return nil, err
		}

		answer, err := peerConnection.AcceptOfferAndCreateAnswer(ctx, offer)
		if err != nil {
			return nil, err
		}

		if err := signaller.PushAnswer(ctx, answer); err != nil {
			return nil, err
		}
	}

	ctxICE, cancelICE := context.WithCancel(ctx)
	go func() {
		defer cancelICE()
		select {
		case <-openChan:
		case <-peerConnection.Closed():
		}
	}()

	go func() {

		for {
			iceCandidate, err := signaller.PullICECandidate(ctxICE)
			if err != nil {
				log.Printf("PeerConnection failed to pull ice candidate due to error (%v)", err)
				return
			}

			if err := peerConnection.AddICECandidate(ctx, iceCandidate); err != nil {
				log.Printf("PeerConnection failed to pull ice candidate due to error (%v)", err)
				peerConnection.Close()
				return
			}
		}
	}()

	// Make sure to wait for open / close
	select {
	case <-peerConnection.Closed():
		return nil, ErrPeerConnectionClosed
	case <-openChan:
		return
	}
}

func DialPeerConnection(ctx context.Context, signaller Signaller) (peerConnection *PeerConnection, err error) {
	return newPeerConnection(ctx, true, signaller)
}

func ListenPeerConnection(ctx context.Context, signaller Signaller) (peerConnection *PeerConnection, err error) {
	return newPeerConnection(ctx, false, signaller)
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

////////////////////////////////////////////////////////////////////////
//////////////////////////////// LocalSignaller ////////////////////////
////////////////////////////////////////////////////////////////////////

type LocalSignaller struct {
	offerChan        chan interface{}
	answerChan       chan interface{}
	iceCandidateChan chan interface{}
}

func NewLocalSignaller() *LocalSignaller {
	return &LocalSignaller{
		make(chan interface{}),
		make(chan interface{}),
		make(chan interface{}),
	}
}

func (s *LocalSignaller) PushOffer(ctx context.Context, offer interface{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.offerChan <- offer:
		return nil
	}
}

func (s *LocalSignaller) PullOffer(ctx context.Context) (interface{}, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case offer := <-s.offerChan:
		return offer, nil
	}
}

func (s *LocalSignaller) PushAnswer(ctx context.Context, answer interface{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.answerChan <- answer:
		return nil
	}
}

func (s *LocalSignaller) PullAnswer(ctx context.Context) (interface{}, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case answer := <-s.answerChan:
		return answer, nil
	}
}

func (s *LocalSignaller) PushICECandidate(ctx context.Context, iceCanidate interface{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.iceCandidateChan <- iceCanidate:
		return nil
	}
}

func (s *LocalSignaller) PullICECandidate(ctx context.Context) (interface{}, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case iceCandidate := <-s.iceCandidateChan:
		return iceCandidate, nil
	}
}

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Main //////////////////////////////////
////////////////////////////////////////////////////////////////////////

func main() {
	ctx := context.Background()
	signaller := NewLocalSignaller()

	go func() {
		c2, err := ListenPeerConnection(ctx, signaller)
		if err != nil {
			panic(err)
		}

		transferred := 0
		buf := make([]byte, 1024*1024)
		for {
			n, err := c2.DataChannel.Read(buf)
			if err != nil {
				panic(err)
			}
			transferred += n
			log.Printf("Read bytes (%d) and total bytes (%d) with error (%v)", n, transferred, err)
		}
	}()

	c1, err := DialPeerConnection(ctx, signaller)
	if err != nil {
		panic(err)
	}

	b := make([]byte, chunkSize)
	_, err = io.ReadFull(rand.Reader, b)
	if err != nil {
		panic(err)
	}

	var dest bytes.Buffer
	for i := 0; i < 10000; i++ {
		dest.Write(b)
	}

	n, err := c1.DataChannel.Write(dest.Bytes())
	log.Printf("Written bytes (%d) with error (%v)", n, err)
}
