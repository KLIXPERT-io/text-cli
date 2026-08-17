package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

// metricInfo is the discovery record for one metric. It is what an agent reads
// to find out what this CLI can measure, so it is built from the registry and
// never from a hand-maintained list.
type metricInfo struct {
	Name        string   `json:"name"`
	Title       string   `json:"title,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Languages   []string `json:"languages"`
	Description string   `json:"description,omitempty"`
}

func newMetricInfo(m analyze.Metric) metricInfo {
	langs := m.Languages
	if langs == nil {
		langs = []string{}
	}
	return metricInfo{
		Name:        m.Name,
		Title:       m.Title,
		Aliases:     m.Aliases,
		Languages:   langs,
		Description: m.Description,
	}
}

func (mi metricInfo) row() output.Row {
	return output.Row{
		"name":        mi.Name,
		"title":       mi.Title,
		"languages":   strings.Join(mi.Languages, ","),
		"aliases":     strings.Join(mi.Aliases, ","),
		"description": mi.Description,
	}
}

func newMetricsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "metrics",
		Short: "Discover the registered text metrics",
		Long: `metrics lists what this binary can measure and in which languages.

Run bare it behaves like ` + "`text metrics list`" + `.

Examples:
  text metrics
  text metrics list --output json
  text metrics show flesch-de`,
		Args: cobra.NoArgs,
		RunE: runMetricsList,
	}
	c.AddCommand(newMetricsListCmd(), newMetricsShowCmd())
	return c
}

func newMetricsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List every registered metric",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE:    runMetricsList,
	}
}

func runMetricsList(cmd *cobra.Command, _ []string) error {
	all := analyze.All()
	infos := make([]metricInfo, 0, len(all))
	rows := make([]output.Row, 0, len(all))
	records := make([]any, 0, len(all))
	for _, m := range all {
		mi := newMetricInfo(m)
		infos = append(infos, mi)
		rows = append(rows, mi.row())
		records = append(records, mi)
	}
	return emitResult(cmd, emitOpts{
		Data:    map[string]any{"metrics": infos},
		Meta:    output.Meta{},
		Columns: []string{"name", "title", "languages", "description"},
		Rows:    rows,
		Records: records,
		Text: func(w io.Writer) error {
			for _, mi := range infos {
				if _, err := fmt.Fprintf(w, "%s  [%s]  %s\n", mi.Name, strings.Join(mi.Languages, ","), mi.Description); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

func newMetricsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show one metric in full, resolving aliases",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, ok := analyze.Get(args[0])
			if !ok {
				return errs.Newf(errs.CodeUnknownMetric, "unknown metric: %q", args[0]).
					WithHint("Known metrics: " + strings.Join(analyze.Names(), ", ") + ". Run `text metrics list` for details.")
			}
			mi := newMetricInfo(m)
			return emitResult(cmd, emitOpts{
				Data:    mi,
				Meta:    output.Meta{},
				Columns: []string{"name", "title", "languages", "aliases", "description"},
				Rows:    []output.Row{mi.row()},
				Records: []any{mi},
				Text: func(w io.Writer) error {
					fmt.Fprintf(w, "name:        %s\n", mi.Name)
					fmt.Fprintf(w, "title:       %s\n", mi.Title)
					fmt.Fprintf(w, "languages:   %s\n", strings.Join(mi.Languages, ", "))
					if len(mi.Aliases) > 0 {
						fmt.Fprintf(w, "aliases:     %s\n", strings.Join(mi.Aliases, ", "))
					}
					_, err := fmt.Fprintf(w, "description: %s\n", mi.Description)
					return err
				},
			})
		},
	}
}
