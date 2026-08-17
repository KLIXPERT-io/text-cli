package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/update"
	"github.com/spf13/cobra"
)

func newUpdateCmd(version string) *cobra.Command {
	c := &cobra.Command{
		Use:   "update",
		Short: "Manage text self-update (status, check, apply)",
	}
	c.AddCommand(
		newUpdateStatusCmd(version),
		newUpdateCheckCmd(version),
		newUpdateApplyCmd(version),
	)
	return c
}

func resolveExecPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		return r
	}
	return exe
}

func autoUpdateStatus(cfg *config.Config, execPath string) (enabled bool, reason string) {
	if v, ok := os.LookupEnv("TEXT_NO_UPDATE"); ok && v != "" {
		switch v {
		case "0", "false":
		default:
			return false, "env:TEXT_NO_UPDATE"
		}
	}
	if cfg != nil && !cfg.AutoUpdate {
		return false, "config:auto_update=false"
	}
	if execPath != "" && update.IsManagedInstall(execPath) {
		return false, "managed-install"
	}
	return true, ""
}

func newUpdateStatusCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current/latest version, last check, and auto-update state",
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			ctx := cmd.Context()

			st, _ := update.LoadState()
			execPath := resolveExecPath()

			var cfg *config.Config
			if s != nil {
				cfg = s.Cfg
			}
			enabled, disabledReason := autoUpdateStatus(cfg, execPath)

			latest := ""
			latestErr := ""
			if tag, err := update.LatestRelease(ctx, version); err == nil {
				latest = tag
			} else {
				latest = "unknown"
				latestErr = err.Error()
			}

			data := map[string]any{
				"current_version":        version,
				"latest_version":         latest,
				"channel":                "stable",
				"last_check_at":          timeOrEmpty(st.LastCheckAt),
				"last_installed_version": st.LastInstalledVersion,
				"last_installed_at":      timeOrEmpty(st.LastInstalledAt),
				"auto_update_enabled":    enabled,
				"install_path":           execPath,
				"install_managed":        update.IsManagedInstall(execPath),
			}
			if !enabled {
				data["disabled_reason"] = disabledReason
			}
			if latestErr != "" && s != nil && s.Verbose {
				data["latest_error"] = latestErr
			}

			rows := []output.Row{
				{"key": "current_version", "value": version},
				{"key": "latest_version", "value": latest},
				{"key": "channel", "value": "stable"},
				{"key": "last_check_at", "value": timeRFC3339(st.LastCheckAt)},
				{"key": "last_installed_version", "value": st.LastInstalledVersion},
				{"key": "last_installed_at", "value": timeRFC3339(st.LastInstalledAt)},
				{"key": "auto_update_enabled", "value": enabled},
				{"key": "install_path", "value": execPath},
				{"key": "install_managed", "value": update.IsManagedInstall(execPath)},
			}
			if !enabled {
				rows = append(rows, output.Row{"key": "disabled_reason", "value": disabledReason})
			}
			if latestErr != "" && s != nil && s.Verbose {
				rows = append(rows, output.Row{"key": "latest_error", "value": latestErr})
			}

			return emitResult(cmd, emitOpts{
				Data:    data,
				Columns: []string{"key", "value"},
				Rows:    rows,
				Text: func(w io.Writer) error {
					for _, r := range rows {
						if _, err := fmt.Fprintf(w, "%s: %v\n", r["key"], r["value"]); err != nil {
							return err
						}
					}
					return nil
				},
			})
		},
	}
}

// timeOrEmpty renders a zero time.Time as "" rather than the Go zero-value
// timestamp, so `text update status --output json` doesn't print
// "0001-01-01T00:00:00Z" for a field that has simply never been set.
func timeOrEmpty(t interface{ IsZero() bool }) any {
	if t.IsZero() {
		return ""
	}
	return t
}

// timeRFC3339 is the table/text-output counterpart of timeOrEmpty.
func timeRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func newUpdateCheckCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Force an update check now (bypasses the 24h throttle)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			res, err := update.CheckAndApply(ctx, version, true)
			if err != nil {
				fmt.Fprintf(os.Stderr, "text: update check failed: %v\n", err)
			}
			data := map[string]any{
				"updated": res.Updated,
				"from":    res.From,
				"to":      res.To,
				"reason":  res.Reason,
			}
			rows := []output.Row{
				{"key": "updated", "value": res.Updated},
				{"key": "from", "value": res.From},
				{"key": "to", "value": res.To},
				{"key": "reason", "value": res.Reason},
			}
			if err != nil {
				data["error"] = err.Error()
				rows = append(rows, output.Row{"key": "error", "value": err.Error()})
			}
			return emitResult(cmd, emitOpts{
				Data:    data,
				Columns: []string{"key", "value"},
				Rows:    rows,
				Text: func(w io.Writer) error {
					for _, r := range rows {
						if _, err := fmt.Fprintf(w, "%s: %v\n", r["key"], r["value"]); err != nil {
							return err
						}
					}
					return nil
				},
			})
		},
	}
}

func newUpdateApplyCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Force download + swap to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			tag, err := update.LatestRelease(ctx, version)
			if err != nil {
				return errs.Newf(errs.CodeNetworkUnreachable, "resolve latest release: %v", err)
			}
			res, err := update.Apply(ctx, version, tag)
			if err != nil {
				return errs.Newf(errs.CodeGeneric, "apply update %s: %v", tag, err)
			}
			if res.Updated {
				fmt.Fprintf(os.Stdout, "text: updated to %s (was %s)\n", res.To, res.From)
			} else {
				fmt.Fprintf(os.Stdout, "text: no update applied (%s)\n", res.Reason)
			}
			return nil
		},
	}
}
