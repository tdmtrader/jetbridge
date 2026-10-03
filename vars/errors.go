package vars

import (
	"fmt"
	"strings"
)

type UndefinedVarsError struct {
	Vars []string
}

func (err UndefinedVarsError) Error() string {
	return fmt.Sprintf("undefined vars: %s", strings.Join(err.Vars, ", "))
}

type unusedVarsError struct {
	Vars []string
}

func (err unusedVarsError) Error() string {
	return fmt.Sprintf("unused vars: %s", strings.Join(err.Vars, ", "))
}

type missingSourceError struct {
	Name   string
	Source string
}

func (err missingSourceError) Error() string {
	return fmt.Sprintf("missing source '%s' in var: %s", err.Source, err.Name)
}

type MissingFieldError struct {
	Name  string
	Field string
}

func (err MissingFieldError) Error() string {
	return fmt.Sprintf("missing field '%s' in var: %s", err.Field, err.Name)
}

type InvalidFieldError struct {
	Name  string
	Field string
	Value any
}

func (err InvalidFieldError) Error() string {
	return fmt.Sprintf("cannot access field '%s' of non-map value ('%T') from var: %s", err.Field, err.Value, err.Name)
}

type invalidInterpolationError struct {
	Name  string
	Value any
}

func (err invalidInterpolationError) Error() string {
	return fmt.Sprintf("cannot interpolate non-primitive value (%T) from var: %s", err.Value, err.Name)
}
