// Package dep resolves recipe dependency order.
package dep

import "fmt"

// Loader returns the dependency list for a package name.
type Loader func(name string) ([]string, error)

// Order returns targets and their transitive dependencies in build order
// (dependencies before dependents). Each name appears exactly once.
func Order(targets []string, load Loader) ([]string, error) {
	var order []string
	state := make(map[string]int) // 0=unseen, 1=visiting, 2=done

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("dependency cycle involving %q", name)
		case 2:
			return nil
		}
		state[name] = 1
		deps, err := load(name)
		if err != nil {
			return err
		}
		for _, d := range deps {
			if err := visit(d); err != nil {
				return err
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}

	for _, t := range targets {
		if err := visit(t); err != nil {
			return nil, err
		}
	}
	return order, nil
}
