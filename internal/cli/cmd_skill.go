package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	hlpskill "github.com/maxmoneycash/hlp-cli/skills/hlp"
)

// skillDirs are where each agent reads user-level skills, relative to $HOME.
var skillDirs = map[string]string{
	"claude": ".claude/skills/hlp",
	"codex":  ".codex/skills/hlp",
	"cursor": ".cursor/skills/hlp",
}

// skillCmd prints the agent skill (skills/hlp/SKILL.md, embedded) or
// installs it for one agent.
func (a *app) skillCmd() *cobra.Command {
	var install string
	cmd := &cobra.Command{
		Use:     "skill",
		Short:   "Print the hlp agent skill, or install it for Claude, Codex or Cursor",
		Long:    "Print the hlp agent skill (SKILL.md) to stdout.\n\n  hlp skill --install claude   writes ~/.claude/skills/hlp/SKILL.md\n  hlp skill --install codex    writes ~/.codex/skills/hlp/SKILL.md\n  hlp skill --install cursor   writes ~/.cursor/skills/hlp/SKILL.md",
		GroupID: "meta",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if install == "" {
				_, err := fmt.Fprint(a.io.Out, hlpskill.Markdown)
				return err
			}
			rel, ok := skillDirs[strings.ToLower(install)]
			if !ok {
				return &core.Error{Code: core.CodeUsage, Message: fmt.Sprintf("--install takes claude, codex or cursor, got %q", install)}
			}
			home := a.io.Getenv("HOME")
			if home == "" {
				var err error
				if home, err = os.UserHomeDir(); err != nil {
					return err
				}
			}
			dir := filepath.Join(home, filepath.FromSlash(rel))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			path := filepath.Join(dir, "SKILL.md")
			if err := os.WriteFile(path, []byte(hlpskill.Markdown), 0o644); err != nil {
				return err
			}
			_, err := fmt.Fprintf(a.io.Err, "wrote %s\n", path)
			return err
		},
	}
	cmd.Flags().StringVar(&install, "install", "", "Install for an agent: claude, codex or cursor")
	return cmd
}
