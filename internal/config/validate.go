package config

import (
	"fmt"
	"regexp"
	"strings"
)

var stepIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (f File) Validate() error {
	if f.Version != 1 {
		return fmt.Errorf("version must be 1")
	}

	if f.Planner != nil {
		if err := validateCommand("planner.command", f.Planner.Command); err != nil {
			return err
		}
		if len(f.Steps) > 0 {
			return fmt.Errorf("configure either planner or steps, not both")
		}
		return nil
	}

	return validateSteps(f.Steps, true)
}

func validateSteps(steps []Step, requireNonEmpty bool) error {
	if requireNonEmpty && len(steps) == 0 {
		return fmt.Errorf("at least one step is required when no planner is configured")
	}

	seen := make(map[string]Step, len(steps))
	for _, step := range steps {
		if err := validateStep(step); err != nil {
			return err
		}
		if _, ok := seen[step.ID]; ok {
			return fmt.Errorf("duplicate step id %q", step.ID)
		}
		seen[step.ID] = step
	}

	for _, step := range steps {
		seenNeeds := make(map[string]struct{}, len(step.Needs))
		for _, need := range step.Needs {
			if need == step.ID {
				return fmt.Errorf("step %q cannot depend on itself", step.ID)
			}
			if _, ok := seen[need]; !ok {
				return fmt.Errorf("step %q needs unknown step %q", step.ID, need)
			}
			if _, ok := seenNeeds[need]; ok {
				return fmt.Errorf("step %q declares duplicate dependency %q", step.ID, need)
			}
			seenNeeds[need] = struct{}{}
		}
	}

	if err := validateAcyclic(seen); err != nil {
		return err
	}

	return nil
}

func validateStep(step Step) error {
	if step.ID == "" {
		return fmt.Errorf("step id is required")
	}
	if !stepIDPattern.MatchString(step.ID) {
		return fmt.Errorf("step %q has invalid id", step.ID)
	}
	if err := validateCommand(fmt.Sprintf("step %q command", step.ID), step.Command); err != nil {
		return err
	}
	if step.If != "" && step.If != "true" && step.If != "false" {
		return fmt.Errorf("step %q if must be empty, \"true\", or \"false\"", step.ID)
	}
	return nil
}

func validateCommand(name string, command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("%s must not be empty", name)
	}
	if strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("%s must start with a non-empty executable", name)
	}
	return nil
}

func validateAcyclic(steps map[string]Step) error {
	const (
		unvisited = iota
		visiting
		visited
	)

	state := make(map[string]int, len(steps))
	var visit func(string, []string) error
	visit = func(stepID string, stack []string) error {
		switch state[stepID] {
		case visited:
			return nil
		case visiting:
			cycle := appendCycle(stack, stepID)
			return fmt.Errorf("dependency cycle detected: %s", strings.Join(cycle, " -> "))
		}

		state[stepID] = visiting
		stack = append(stack, stepID)
		for _, need := range steps[stepID].Needs {
			if err := visit(need, stack); err != nil {
				return err
			}
		}
		state[stepID] = visited
		return nil
	}

	for stepID := range steps {
		if err := visit(stepID, nil); err != nil {
			return err
		}
	}

	return nil
}

func appendCycle(stack []string, repeated string) []string {
	for i, stepID := range stack {
		if stepID == repeated {
			cycle := append([]string(nil), stack[i:]...)
			return append(cycle, repeated)
		}
	}
	return []string{repeated, repeated}
}
