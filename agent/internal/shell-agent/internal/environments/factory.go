package environments

import (
	"fmt"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

type options struct {
	persistTmpData bool
}

// Option configures environment construction.
type Option func(*options)

// WithPersistTmpData instructs the environment to keep temporary data on Close.
func WithPersistTmpData(v bool) Option {
	return func(o *options) { o.persistTmpData = v }
}

// NewEnvironment constructs an Environment implementation based on configuration.
func NewEnvironment(cfg *minisweagent.EnvironmentConfig, optFns ...Option) (minisweagent.Environment, error) {
	if cfg == nil {
		local, err := NewLocalEnvironment(nil)
		if err != nil {
			return nil, err
		}
		return NewForgeWrapper(local), nil
	}

	var opts options
	for _, fn := range optFns {
		if fn != nil {
			fn(&opts)
		}
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "local":
		local, err := NewLocalEnvironment(cfg)
		if err != nil {
			return nil, err
		}
		return NewForgeWrapper(local), nil
	default:
		return nil, fmt.Errorf("unsupported environment type %q", cfg.Type)
	}
}
