package strategy

import "fmt"

var registry = make(map[string]Strategy)

// Register a strategy with a given name.
func Register(name string, s Strategy) {
	registry[name] = s
}

// Get a strategy by its name.
func Get(name string) (Strategy, error) {
	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("strategy not found: %s", name)
	}
	return s, nil
}
