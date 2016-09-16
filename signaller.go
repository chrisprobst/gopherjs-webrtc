package main

import "context"

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Signaller /////////////////////////////
////////////////////////////////////////////////////////////////////////

type Command string

const (
	CmdOffer Command = "offer"
	CmdICE   Command = "ice"
)

type SignalHandler func(ctx context.Context, remoteID string, command Command, req interface{}) (res interface{}, err error)

type Signaller interface {
	Signal(ctx context.Context, remoteID string, command Command, req interface{}) (res interface{}, err error)
	OnSignal(signalHandler SignalHandler)
}
