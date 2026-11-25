package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	xterm "github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/exchange"
)

// AuthStatus is `hlp auth status` output. The key itself is never shown:
// only where it comes from and the public address it signs as.
type AuthStatus struct {
	AgentKey   string `json:"agentKey"`             // set | unset
	Source     string `json:"source,omitempty"`     // env | keychain
	Address    string `json:"address,omitempty"`    // the API wallet's public address
	Trading    bool   `json:"tradingEnabled"`       // config trading.enabled
	LiveBuild  bool   `json:"liveBuild"`            // this build can send orders at all
	AccountFor string `json:"accountFor,omitempty"` // trading.account
}

func (a *app) authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Agent (API wallet) key: status, set (OS keychain), remove",
		Long: "hlp signs with an Hyperliquid API wallet (agent) key, never your master key. An agent key\n" +
			"can trade but cannot withdraw. Create one at app.hyperliquid.xyz/API, then either export\n" +
			"HLP_AGENT_KEY or store it in the OS keychain with `hlp auth set` (read from stdin, never argv).",
		GroupID: "meta",
	}
	cmd.AddCommand(a.authStatusCmd(), a.authSetCmd(), a.authRemoveCmd())
	return cmd
}

func (a *app) authStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether an agent key is set, where from, and its public address",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := a.mode()
			if err != nil {
				return err
			}
			env, err := a.env(cmd.Context())
			if err != nil {
				return err
			}
			defer env.Close()
			st := AuthStatus{AgentKey: "unset", Trading: env.Config.Trading.Enabled, LiveBuild: exchange.LiveSendCompiled(),
				AccountFor: env.Config.Trading.AccountAddr}
			if s, err := env.Secrets.Get(config.SecretAgentKey); err == nil && !s.Empty() {
				st.AgentKey, st.Source = "set", "keychain"
				if strings.TrimSpace(os.Getenv(config.EnvFor(config.SecretAgentKey))) != "" {
					st.Source = "env"
				}
				if signer, err := exchange.NewSigner(s.Reveal()); err == nil {
					st.Address = signer.Address()
				} else {
					st.Address = "invalid key: " + err.Error()
				}
			}
			if m.JSON {
				return writeResult(a.io.Out, &core.Result{Data: st, Meta: domain.Meta{Source: "auth", AsOf: time.Now().UTC()}}, m)
			}
			if st.AgentKey == "unset" {
				_, err = fmt.Fprintln(a.io.Out, "agent key: not set (export HLP_AGENT_KEY or run `hlp auth set`)")
				return err
			}
			fmt.Fprintf(a.io.Out, "agent key: set (%s)\naddress:   %s\ntrading:   %v (config trading.enabled)\n", st.Source, st.Address, st.Trading)
			if !st.LiveBuild {
				fmt.Fprintln(a.io.Out, "this build is dry-run only: orders are signed and shown, never sent")
			}
			return nil
		},
	}
}

func (a *app) authSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set",
		Short: "Store an agent key in the OS keychain (reads it from stdin; never pass keys as arguments)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, err := a.readSecret("Agent (API wallet) private key: ")
			if err != nil {
				return err
			}
			signer, err := exchange.NewSigner(key)
			if err != nil {
				return &core.Error{Code: core.CodeUsage, Message: "that is not a valid secp256k1 private key", Hint: "paste the 0x… key of an API wallet from app.hyperliquid.xyz/API"}
			}
			if err := config.NewSecrets().Set(config.SecretAgentKey, config.NewSecret(key)); err != nil {
				return &core.Error{Code: core.CodeUsage, Message: err.Error(), Hint: "export HLP_AGENT_KEY instead"}
			}
			_, err = fmt.Fprintf(a.io.Out, "stored in the OS keychain (service %q). Agent address: %s\n", config.KeychainService, signer.Address())
			return err
		},
	}
}

func (a *app) authRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Delete the agent key from the OS keychain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := config.NewSecrets().Delete(config.SecretAgentKey)
			if errors.Is(err, config.ErrSecretNotFound) {
				_, err = fmt.Fprintln(a.io.Out, "no agent key in the keychain")
				return err
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(a.io.Out, "agent key removed from the keychain")
			return err
		},
	}
}

// readSecret reads one line from stdin without echo on a terminal.
func (a *app) readSecret(prompt string) (string, error) {
	var line string
	if f, ok := a.io.In.(*os.File); ok && a.io.InTTY && xterm.IsTerminal(f.Fd()) {
		fmt.Fprint(a.io.Err, prompt)
		b, err := xterm.ReadPassword(f.Fd())
		fmt.Fprintln(a.io.Err)
		if err != nil {
			return "", err
		}
		line = string(b)
	} else {
		s, err := bufio.NewReader(a.io.In).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		line = s
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", &core.Error{Code: core.CodeUsage, Message: "no key on stdin", Hint: "pipe it in: `pbpaste | hlp auth set`, or run it on a terminal to be prompted"}
	}
	return line, nil
}
