package cmd

import (
	"fmt"
	"io"

	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Read/write the text config file"}
	c.AddCommand(newConfigGetCmd(), newConfigSetCmd(), newConfigPathCmd(), newConfigListCmd())
	return c
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Read a config value by dotted key (e.g. defaults.lang)",
		Long: `Examples:
  text config get defaults.lang
  text config get entities.service_account_path`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			val, ok := cfg.Get(args[0])
			if !ok {
				return errs.New(errs.CodeInvalidArgs, "unknown key: "+args[0]).WithHint("Try `text config list`.")
			}
			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"key": args[0], "value": val},
				Columns: []string{"key", "value"},
				Rows:    []output.Row{{"key": args[0], "value": val}},
				Text:    func(w io.Writer) error { _, err := fmt.Fprintln(w, val); return err },
			})
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Write a config value",
		Long: `Examples:
  text config set defaults.lang de
  text config set defaults.output json
  text config set entities.service_account_path ~/secrets/text-sa.json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := cfg.Set(args[0], args[1]); err != nil {
				return errs.New(errs.CodeInvalidArgs, err.Error()).WithHint("Try `text config list`.")
			}
			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"ok": true, "key": args[0], "value": args[1]},
				Columns: []string{"key", "value"},
				Rows:    []output.Row{{"key": args[0], "value": args[1]}},
			})
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path of the config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := config.Path()
			if err != nil {
				return err
			}
			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"path": p},
				Columns: []string{"path"},
				Rows:    []output.Row{{"path": p}},
				Text:    func(w io.Writer) error { _, err := fmt.Fprintln(w, p); return err },
			})
		},
	}
}

func newConfigListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every config key and its current value",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			values := map[string]string{}
			rows := []output.Row{}
			for _, k := range config.Keys() {
				v, _ := cfg.Get(k)
				values[k] = v
				rows = append(rows, output.Row{"key": k, "value": v})
			}
			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"path": cfg.LoadedPath(), "values": values},
				Columns: []string{"key", "value"},
				Rows:    rows,
			})
		},
	}
}
