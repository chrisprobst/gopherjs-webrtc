package main

import "context"

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Signaller /////////////////////////////
////////////////////////////////////////////////////////////////////////

type ICESignaller interface {
	RequestICECandidate(context.Context) (interface{}, error)
}

type DialSignaller interface {
	ICESignaller
	PushOffer(context.Context, interface{}) error
	PullAnswer(context.Context) (interface{}, error)
}

type ListenSignaller interface {
	ICESignaller
	PullOffer(context.Context) (interface{}, error)
	PushAnswer(context.Context, interface{}) error
}
