package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/wen/opentalon/internal/runlease"
	"github.com/wen/opentalon/internal/storage"
)

const runLeaseDuration = 30 * time.Second

type runSession struct {
	token  runlease.Token
	store  runlease.Store
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func acquireRun(ctx context.Context, db *storage.Storage, runID string) (*runSession, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	store := db.RunLeases()
	token, err := store.Acquire(ctx, runID, hex.EncodeToString(id[:]), runLeaseDuration)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancelCause(ctx)
	s := &runSession{token: token, store: store, ctx: child, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(runLeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-ticker.C:
				renewCtx, stop := context.WithTimeout(child, 5*time.Second)
				err := store.Renew(renewCtx, token, runLeaseDuration)
				stop()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return s, nil
}
func (s *runSession) close() {
	s.cancel(context.Canceled)
	<-s.done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.store.Release(ctx, s.token)
}
