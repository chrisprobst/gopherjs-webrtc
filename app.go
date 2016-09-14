package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log"
)

////////////////////////////////////////////////////////////////////////
//////////////////////////////// LocalSignaller ////////////////////////
////////////////////////////////////////////////////////////////////////

type LocalDialSignaller struct {
	localPeerConnection  *PeerConnection
	remotePeerConnection *PeerConnection
	offerChan            chan interface{}
	answerChan           chan interface{}
}

type LocalListenSignaller struct {
	localPeerConnection  *PeerConnection
	remotePeerConnection *PeerConnection
	offerChan            chan interface{}
	answerChan           chan interface{}
}

func NewLocalSignallers(a, b *PeerConnection) (*LocalDialSignaller, *LocalListenSignaller) {
	offerChan := make(chan interface{})
	answerChan := make(chan interface{})
	dialSignaller := &LocalDialSignaller{a, b, offerChan, answerChan}
	listenSignaller := &LocalListenSignaller{b, a, offerChan, answerChan}
	return dialSignaller, listenSignaller
}

func (s *LocalDialSignaller) PushOffer(ctx context.Context, offer interface{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.offerChan <- offer:
		return nil
	}
}

func (s *LocalListenSignaller) PullOffer(ctx context.Context) (interface{}, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case offer := <-s.offerChan:
		return offer, nil
	}
}

func (s *LocalListenSignaller) PushAnswer(ctx context.Context, answer interface{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.answerChan <- answer:
		return nil
	}
}

func (s *LocalDialSignaller) PullAnswer(ctx context.Context) (interface{}, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case answer := <-s.answerChan:
		return answer, nil
	}
}

func (s *LocalDialSignaller) RequestICECandidate(ctx context.Context) (interface{}, error) {
	return s.remotePeerConnection.WaitForICECandidate(ctx)
}

func (s *LocalListenSignaller) RequestICECandidate(ctx context.Context) (interface{}, error) {
	return s.remotePeerConnection.WaitForICECandidate(ctx)
}

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Main //////////////////////////////////
////////////////////////////////////////////////////////////////////////

func main() {
	ctx := context.Background()

	c1, err := NewPeerConnection(ctx)
	if err != nil {
		panic(err)
	}

	c2, err := NewPeerConnection(ctx)
	if err != nil {
		panic(err)
	}

	dialSignaller, listenSignaller := NewLocalSignallers(c1, c2)

	go func() {
		if err := c2.Listen(ctx, listenSignaller); err != nil {
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

	if err := c1.Dial(ctx, dialSignaller); err != nil {
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
