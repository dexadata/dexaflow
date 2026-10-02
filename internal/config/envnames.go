package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"

	"github.com/dexadata/dexaflow/internal/envcompat"
)

// bindBothPrefixes binds every key viper knows to its DEXAFLOW_* variable and,
// as a fallback, its pre-rename LEOFLOW_* variable, in that order: viper reads
// the first one that is set, so the current name wins when both are. This holds
// for any caller of the loaders, not only for binaries that mirrored their
// environment at startup (envcompat.MirrorProcess).
func bindBothPrefixes(v *viper.Viper, replacer *strings.Replacer) error {
	for _, key := range v.AllKeys() {
		suffix := strings.ToUpper(replacer.Replace(key))
		if err := v.BindEnv(key, envcompat.NewPrefix+suffix, envcompat.LegacyPrefix+suffix); err != nil {
			return fmt.Errorf("binding environment for %q: %w", key, err)
		}
	}
	return nil
}
