// Package runlease defines exclusive run ownership. Epochs fence stale processes.
package runlease

import (
	"context"
	"errors"
	"time"
)

var ErrBusy = errors.New("run already owned")
var ErrLost = errors.New("run ownership lost")

type Token struct {
	RunID   string
	OwnerID string
	Epoch   uint64
}
type Store interface {
	Acquire(context.Context, string, string, time.Duration) (Token, error)
	Renew(context.Context, Token, time.Duration) error
	Release(context.Context, Token) error
}
