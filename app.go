package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log"
)

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

func DialPeerConnection(ctx context.Context, signaller Signaller) (peerConnection *PeerConnection, err error) {
	peerConnection, err = NewPeerConnection(ctx)
	if err != nil {
		return nil, err
	}

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

	ctxICE, cancelICE := context.WithCancel(ctx)
	go func() {
		defer cancelICE()
		<-peerConnection.Closed()
	}()

	// Pull remote ice candidates and add them locally
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

	// Push our ice candidates to the remote
	go func() {
		for {
			select {
			case iceCandidate := <-peerConnection.ICECandidates():
				if err := signaller.PushICECandidate(ctx, iceCandidate); err != nil {
					log.Printf("PeerConnection failed to push ice candidate due to error (%v)", err)
					peerConnection.Close()
					return
				}
			case <-ctxICE.Done():
				return
			}
		}
	}()

	select {
	case <-peerConnection.Closed():
		return nil, ErrPeerConnectionClosed
	case <-peerConnection.Open():
		return
	}
}

func ListenPeerConnection(ctx context.Context, signaller Signaller) (peerConnection *PeerConnection, err error) {
	peerConnection, err = NewPeerConnection(ctx)
	if err != nil {
		return nil, err
	}

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

	ctxICE, cancelICE := context.WithCancel(ctx)
	go func() {
		defer cancelICE()
		<-peerConnection.Closed()
	}()

	// Pull remote ice candidates and add them locally
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

	// Push our ice candidates to the remote
	go func() {
		for {
			select {
			case iceCandidate := <-peerConnection.ICECandidates():
				if err := signaller.PushICECandidate(ctx, iceCandidate); err != nil {
					log.Printf("PeerConnection failed to push ice candidate due to error (%v)", err)
					peerConnection.Close()
					return
				}
			case <-ctxICE.Done():
				return
			}
		}
	}()

	select {
	case <-peerConnection.Closed():
		return nil, ErrPeerConnectionClosed
	case <-peerConnection.Open():
		return
	}
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
