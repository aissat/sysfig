package manager

// Package represents a single installed or installable package.
type Package struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version,omitempty"`
}

// PackageManager is the interface every backend must implement.
type PackageManager interface {
	// Name returns the canonical backend name (e.g. "pacman", "apt").
	Name() string

	// IsAvailable reports whether the manager binary is present on $PATH.
	IsAvailable() bool

	// ListExplicit returns all packages explicitly installed by the user
	// (i.e. not pulled in solely as dependencies).
	ListExplicit() ([]Package, error)

	// ListForeign returns packages not from an official repository
	// (e.g. AUR packages for pacman). Returns nil if not applicable.
	ListForeign() ([]Package, error)

	// Install installs the given packages. Names without versions are
	// installed at the latest available version.
	Install(pkgs []Package) error
}
