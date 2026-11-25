package image

import (
	"fmt"
	"strconv"
	"strings"
)

// Environment variables that configure the registry. The Makefile, the
// continuum CLI and the operator read the same names.
const (
	// EnvRegistry names the developer registry, host[:port][/path].
	EnvRegistry = "CONTINUUM_REGISTRY"
	// EnvInsecure is true when the registry speaks plain HTTP.
	EnvInsecure = "CONTINUUM_REGISTRY_INSECURE"
)

// Registry returns the registry by precedence: flag, then environment, then
// the Makefile default. It is empty when none is set.
func Registry(flag string, env func(string) string, def string) string {
	if flag != "" {
		return flag
	}
	if v := env(EnvRegistry); v != "" {
		return v
	}
	return def
}

// Settings is the registry configuration shared by every client of the dev
// registry. One Insecure value feeds all of them (see KoFlags, Resolver and
// DaemonHost); containerd on the nodes is configured by the cluster setup.
type Settings struct {
	// Registry is host[:port][/path], or empty when none is configured.
	Registry string
	// Insecure is true when Registry speaks plain HTTP.
	Insecure bool
}

// Option adjusts NewSettings.
type Option func(*settingsOpts)

type settingsOpts struct{ push bool }

// WithPush declares that the caller will push, so an empty registry is an
// error rather than a silent push to a default registry.
func WithPush() Option { return func(o *settingsOpts) { o.push = true } }

// NewSettings resolves the registry and the insecure switch. insecureFlag is
// nil when the flag was not given; a given flag beats the environment.
func NewSettings(flag string, insecureFlag *bool, env func(string) string, def string, opts ...Option) (Settings, error) {
	var o settingsOpts
	for _, f := range opts {
		f(&o)
	}
	s := Settings{Registry: Registry(flag, env, def)}
	if o.push && s.Registry == "" {
		return Settings{}, fmt.Errorf("push requested but no registry: set %s or pass the registry flag", EnvRegistry)
	}
	switch {
	case insecureFlag != nil:
		s.Insecure = *insecureFlag
	case env(EnvInsecure) != "":
		v, err := strconv.ParseBool(env(EnvInsecure))
		if err != nil {
			return Settings{}, fmt.Errorf("parse %s: %w", EnvInsecure, err)
		}
		s.Insecure = v
	}
	return s, nil
}

// Host is the host[:port] part of Registry.
func (s Settings) Host() string {
	h, _, _ := strings.Cut(s.Registry, "/")
	return h
}

// KoFlags are the extra ko flags: --insecure-registry when Insecure.
func (s Settings) KoFlags() []string {
	if s.Registry == "" || !s.Insecure {
		return nil
	}
	return []string{"--insecure-registry"}
}

// Resolver is the operator config: the registry host is listed under
// insecureRegistries only when Insecure.
func (s Settings) Resolver() ResolverConfig {
	if s.Registry == "" || !s.Insecure {
		return ResolverConfig{}
	}
	return ResolverConfig{InsecureRegistries: []string{s.Host()}}
}

// DaemonHost is the host the Docker daemon must list under
// insecure-registries, or empty when none is needed.
func (s Settings) DaemonHost() string {
	if s.Registry == "" || !s.Insecure {
		return ""
	}
	return s.Host()
}
