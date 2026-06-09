package provider

import (
	"fmt"
	"sync"
)

var (
	mu               sync.RWMutex
	energyProviders  = map[string]EnergyProviderFactory{}
	inferenceProviders = map[string]InferenceProviderFactory{}
)

// EnergyProviderFactory creates an EnergyProvider from a config map.
type EnergyProviderFactory func(config map[string]string) (EnergyProvider, error)

// InferenceProviderFactory creates an InferenceMetricsProvider from a config map.
type InferenceProviderFactory func(config map[string]string) (InferenceMetricsProvider, error)

// RegisterEnergy registers an EnergyProvider factory under a given name.
// Call this from an init() function in your provider package.
func RegisterEnergy(name string, factory EnergyProviderFactory) {
	mu.Lock()
	defer mu.Unlock()
	energyProviders[name] = factory
}

// RegisterInference registers an InferenceMetricsProvider factory under a given name.
func RegisterInference(name string, factory InferenceProviderFactory) {
	mu.Lock()
	defer mu.Unlock()
	inferenceProviders[name] = factory
}

// NewEnergy creates an EnergyProvider by name.
func NewEnergy(name string, config map[string]string) (EnergyProvider, error) {
	mu.RLock()
	factory, ok := energyProviders[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown energy provider %q — registered: %v", name, energyProviderNames())
	}
	return factory(config)
}

// NewInference creates an InferenceMetricsProvider by name.
func NewInference(name string, config map[string]string) (InferenceMetricsProvider, error) {
	mu.RLock()
	factory, ok := inferenceProviders[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown inference provider %q — registered: %v", name, inferenceProviderNames())
	}
	return factory(config)
}

func energyProviderNames() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(energyProviders))
	for n := range energyProviders { names = append(names, n) }
	return names
}

func inferenceProviderNames() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(inferenceProviders))
	for n := range inferenceProviders { names = append(names, n) }
	return names
}
