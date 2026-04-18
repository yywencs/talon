package tool

import (
	"context"
	"sync"
)

type ToolFactory func(ctx context.Context) Tool

type ToolRegistry interface {
	Register(name string, factory ToolFactory)
	Resolve(name string, ctx context.Context) (Tool, error)
	Get(name string) (ToolFactory, bool)
	List() []string
}

type toolRegistry struct {
	mu        sync.RWMutex
	factories map[string]ToolFactory
}

var (
	defaultRegistry = &toolRegistry{
		factories: make(map[string]ToolFactory),
	}
)

func Register(name string, factory ToolFactory) {
	defaultRegistry.Register(name, factory)
}

func Resolve(name string, ctx context.Context) (Tool, error) {
	return defaultRegistry.Resolve(name, ctx)
}

func ResolveAll(ctx context.Context) map[string]Tool {
	return defaultRegistry.ResolveAll(ctx)
}

func Get(name string) (ToolFactory, bool) {
	return defaultRegistry.Get(name)
}

func List() []string {
	return defaultRegistry.List()
}

func (r *toolRegistry) Register(name string, factory ToolFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

func (r *toolRegistry) Resolve(name string, ctx context.Context) (Tool, error) {
	r.mu.RLock()
	factory, ok := r.factories[name]
	r.mu.RUnlock()

	if !ok {
		return nil, &ToolNotFoundError{Name: name}
	}

	return factory(ctx), nil
}

func (r *toolRegistry) ResolveAll(ctx context.Context) map[string]Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]Tool, len(r.factories))
	for name, factory := range r.factories {
		result[name] = factory(ctx)
	}
	return result
}

func (r *toolRegistry) Get(name string) (ToolFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[name]
	return factory, ok
}

func (r *toolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

type ToolNotFoundError struct {
	Name string
}

func (e *ToolNotFoundError) Error() string {
	return "tool not found: " + e.Name
}
