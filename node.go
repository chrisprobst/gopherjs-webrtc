package main

import (
	"context"
	"log"
	"sync"
)

type Node struct {
	mtx   sync.Mutex
	peers map[string]*Peer
}

func NewNode() (n *Node) {
	return &Node{}
}

func (n *Node) AcceptPeer(ctx context.Context, remoteID string, offer interface{}) (answer interface{}, err error) {
	return nil, nil
}

func (n *Node) AcceptICECandidate(ctx context.Context, remoteID string, iceCandidate interface{}) error {
	return nil
}

func (n *Node) DialPeer(ctx context.Context, remoteID string, signalHandler SignalHandler) (*Peer, error) {
	// Register peer with node
	n.mtx.Lock()
	if p, exists := n.peers[remoteID]; exists {
		n.mtx.Unlock()
		return p, nil
	}

	p, err := NewPeer(ctx)
	if err != nil {
		n.mtx.Unlock()
		return nil, err
	}

	// Create offer
	offer, err := p.CreateOffer(ctx)
	if err != nil {
		p.Close()
		return nil, err
	}

	answer, err := signalHandler(ctx, remoteID, CmdOffer, offer)
	if err != nil {
		p.Close()
		return nil, err
	}

	if err := p.AcceptAnswer(ctx, answer); err != nil {
		p.Close()
		return nil, err
	}

	go func() {
		ctx, cancel := context.WithCancel(ctx)
		go func() {
			defer cancel()
			<-p.Closed()
		}()

		for {
			iceCandidate, err := p.WaitForICECandidate(ctx)
			if err != nil {
				log.Printf("Peer failed to pull ice candidate due to error (%v)", err)
				p.Close()
				return
			}

			if _, err := signalHandler(ctx, remoteID, CmdICE, iceCandidate); err != nil {
				log.Printf("Peer failed to pull ice candidate due to error (%v)", err)
				p.Close()
				return
			}
		}
	}()

	select {
	case <-p.Closed():
		return nil, ErrPeerClosed
	case <-p.Open():
		return p, nil
	}
}
