package storage

import (
	"fmt"
	"sync"
)

// BackendFactory creates a Backend from a config map.
type BackendFactory func(config map[string]string) (Backend, error)

var (
	mu        sync.RWMutex
	backends  = map[string]BackendFactory{}
)

// Register registers a BackendFactory under name.
// Call this from an init() function in your backend package.
//
// Example:
//
//	func init() {
//		storage.Register("clickhouse", func(cfg map[string]string) (storage.Backend, error) {
//			return New(cfg)
//		})
//	}
func Register(name string, factory BackendFactory) {
	mu.Lock()
	defer mu.Unlock()
	backends[name] = factory
}

// New creates a Backend by name using the registered factory.
// Returns an error if the name is not registered.
func New(name string, config map[string]string) (Backend, error) {
	mu.RLock()
	factory, ok := backends[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown storage backend %q — registered: %v", name, Names())
	}
	return factory(config)
}

// Names returns the names of all registered backends.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(backends))
	for n := range backends {
		names = append(names, n)
	}
	return names
}
