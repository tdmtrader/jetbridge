package component

type Component interface {
	Name() string

	Reload() (bool, error)
}
