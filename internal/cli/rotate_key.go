package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/secrets"
	"github.com/neochaotic/leoflow/internal/storage"
)

// newRotateKeyCommand builds `leoflow lite rotate-key`.
//
// Leoflow Lite encrypted connection secrets with a key compiled into this
// repository, identical on every install, so a datastore file gave up every
// credential in it (#486). Moving off it means re-encrypting what that key
// wrote, and that is a rewrite of every stored credential.
//
// It is a command rather than something the server does on its own at boot, and
// that is the whole design. An implicit migration has to mutate config.yaml
// underneath a running install, on a path nobody asked for, where a partial
// write loses the encryption key, the JWT secret and the admin hash together.
// Here the user asks, one code path does it, and it can be tested end to end.
func newRotateKeyCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Move stored connection secrets onto a new, per-install encryption key.",
		Long: "rotate-key generates a fresh encryption key for THIS install, re-encrypts every " +
			"stored connection secret onto it, and records it in ~/.leoflow/config.yaml.\n\n" +
			"Run it if `leoflow lite` warns that your connections are encrypted with the key " +
			"published in this repository, which every Lite install shares.\n\n" +
			"The datastore must be reachable (start `leoflow lite` once, or leave it running). " +
			"The config file is updated only after every secret has been re-encrypted, so an " +
			"interrupted run leaves the old key in place and can simply be run again.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRotateKey(cmd, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func runRotateKey(cmd *cobra.Command, yes bool) error {
	out := cmd.OutOrStdout()
	home := invokingUserHome()
	if home == "" {
		return fmt.Errorf("could not resolve your home directory")
	}
	configPath := filepath.Join(home, ".leoflow", "config.yaml")
	sec := configFileSecrets(configPath)

	current := sec.secretKey
	if current == "" {
		current = devSecretKey
	}
	newKey, gerr := generateSecretKey()
	if gerr != nil {
		return gerr
	}

	if !yes {
		devPrintln(out, "  This re-encrypts every stored connection secret onto a new key for this install.")
		devPrintln(out, "  Back up ~/.leoflow/config.yaml and your datastore first if you want a way back.")
		if !confirmDestructive(cmd) {
			devPrintln(out, "  aborted; nothing was changed")
			return nil
		}
	}

	ctx := cmdContext(cmd)
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: devDSNs().database})
	if err != nil {
		return fmt.Errorf("connecting to the Lite database (is it up? run `leoflow lite` once): %w", err)
	}
	defer pg.Close()

	cipher, cerr := rotationCipher(newKey, current, sec.secretKeyPrevious)
	if cerr != nil {
		return cerr
	}
	repo := storage.NewRepository(pg)
	repo.SetCipher(cipher)

	n, rerr := repo.ReencryptSecrets(ctx)
	if rerr != nil {
		// The config is NOT advanced. The old key still opens what is still under
		// it, so the install keeps working and the command can be run again once
		// the reported connections are dealt with.
		return fmt.Errorf("re-encrypting (nothing was changed in your config, the previous key still works): %w", rerr)
	}

	// Only now, and with no predecessor: every secret is under the new key, so
	// recording an old one would keep a key alive that nothing needs.
	if werr := persistRotatedKey(configPath, home, sec, newKey); werr != nil {
		return fmt.Errorf("re-encrypted %d connection(s) but could not record the new key in %s. "+
			"Your data is now under a key that is not saved; put `secret_key: %q` in that file before restarting: %w",
			n, configPath, newKey, werr)
	}

	devPrintln(out, fmt.Sprintf("  re-encrypted %d connection secret(s) onto a key only this install has", n))
	devPrintln(out, "  restart `leoflow lite` to pick it up")
	return nil
}

// rotationCipher builds the cipher for the pass: the new key encrypts, the
// current one and any recorded predecessor only decrypt.
func rotationCipher(newKey, current, previous string) (secrets.Cipher, error) {
	list := newKey + "," + current
	if previous != "" && previous != current {
		list += "," + previous
	}
	keys, err := secrets.ParseKeys(list)
	if err != nil {
		return nil, fmt.Errorf("building the rotation keys: %w", err)
	}
	ciphers := make([]secrets.Cipher, 0, len(keys))
	for i, k := range keys {
		c, cerr := secrets.NewAESGCM(k)
		if cerr != nil {
			return nil, fmt.Errorf("key %d: %w", i+1, cerr)
		}
		ciphers = append(ciphers, c)
	}
	return secrets.WithFallback(ciphers[0], ciphers[1:]...), nil
}

// persistRotatedKey records the new key, dropping any predecessor: the pass
// succeeded, so nothing is left for an older key to open.
func persistRotatedKey(configPath, home string, sec liteFileSecrets, newKey string) error {
	c, err := config.Load(configPath, nil)
	if err != nil || c == nil {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}
	return writeLiteConfig(filepath.Join(home, ".leoflow"), c.ParserCmd,
		liteSettings{Workspace: c.Workspace, Executor: c.LiteExecutor, AdminEmail: c.AdminEmail, Port: c.LitePort},
		c.AdminPasswordHash,
		liteFileSecrets{jwtSecret: sec.jwtSecret, secretKey: newKey})
}
