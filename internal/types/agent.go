package types

import "context"

type Agent interface {
	Step(ctx context.Context, state *SessionState) (ObservationEvent, error)
}
