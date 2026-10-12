package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/data"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/firecrawl"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

func newDataCmd() *cobra.Command {
	var (
		source  string
		timeout time.Duration
	)

	c := &cobra.Command{
		Use:   "data",
		Short: "Find and run structured-data tools (Firecrawl Alexandria)",
		Long: `data asks a catalogue of data tools for records a web page only paraphrases:
a company's profile or filings, a person's roles and history, an economic series,
the traffic of a website. Firecrawl's Alexandria catalogue is the source.

It takes three steps, and only the last one costs anything:

  text data find "a listed company's income statement"     # which tools fit, with prices
  text data tool fiscal-ai/financials/income-statement      # one tool's options
  text data run fiscal-ai/financials/income-statement --opt ticker=MSFT

A run costs the credits the catalogue lists for the tool, per call or per
record. It is refused above --max-credits (default 30, config data.max_credits)
before anything is spent, and tools that look up a person's email address or
phone number are refused outright. Answers are cached for 24h.`,
	}

	find := &cobra.Command{
		Use:     "find [query...]",
		Aliases: []string{"search"},
		Short:   "List the tools that can answer a question (free)",
		Long: `find lists catalogue tools matching a description of the data, with their
prices. It runs nothing and costs nothing.

Describe the data, not the entity, in the words a database would use. The
catalogue matches by meaning, and loosely: "a person's profile and career
history" finds the people tools, a bare name finds tools that search social
posts for it, and "employer and job history" finds job boards.

Examples:
  text data find "a company's profile from its domain"
  text data find "which websites send traffic to a domain" --limit 5
  text data find --site https://www.linkedin.com/in/someone
  text data find --provider fred --limit 50`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			flags := cmd.Flags()
			provider, _ := flags.GetString("provider")
			sites, _ := flags.GetStringArray("site")
			limit, _ := flags.GetInt("limit")
			detail, _ := flags.GetBool("detail")
			query := strings.TrimSpace(strings.Join(args, " "))

			opts := data.FindOptions{
				Query:    query,
				Sites:    sites,
				Provider: provider,
				Limit:    limit,
				Detail:   detail,
				Timeout:  timeout,
			}
			src, name, err := s.openDataSource(source)
			if err != nil {
				return err
			}

			sortedSites := append([]string(nil), sites...)
			sort.Strings(sortedSites)
			key := cache.Key("data", []string{
				name, "find", query, strings.Join(sortedSites, "\x00"), strings.ToLower(strings.TrimSpace(provider)),
				strconv.Itoa(opts.EffectiveLimit()), strconv.FormatBool(detail),
			}, "", "")

			var found struct {
				Tools []data.Tool `json:"tools"`
				Total int         `json:"total"`
			}
			entry, err := s.dataCached(key, &found, func() (any, error) {
				tools, total, err := src.FindTools(cmd.Context(), opts)
				if err != nil {
					return nil, err
				}
				return map[string]any{"tools": tools, "total": total}, nil
			})
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data: map[string]any{
					"query":       query,
					"tools":       found.Tools,
					"total":       found.Total,
					"max_credits": s.dataMaxCredits(),
				},
				Meta:    dataMeta(name, len(found.Tools), entry, 1),
				Columns: toolColumns,
				Rows:    toolRows(found.Tools),
				Records: toolRecords(found.Tools),
				Text:    func(w io.Writer) error { return writeToolsText(w, found.Tools) },
			})
		},
	}
	find.Flags().String("provider", "", "list one provider's tools, e.g. fullenrich")
	find.Flags().StringArray("site", nil, "list the tools of the site this address is on (repeatable)")
	find.Flags().Int("limit", data.DefaultLimit, "maximum number of tools (capped at 100)")
	find.Flags().Bool("detail", false, "include every tool's options")

	tool := &cobra.Command{
		Use:   "tool <provider/capability>",
		Short: "Show one tool: price, required inputs, every option (free)",
		Long: `tool reads one catalogue entry in full: what it answers, what it costs, which
inputs it insists on, and every option it takes with type, default and bounds.
That is what to fill a run from. It costs nothing.

Examples:
  text data tool fullenrich/people/lookup
  text data tool fiscal-ai/financials/income-statement --output text`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			provider, capability, err := data.ParseID(args[0])
			if err != nil {
				return err
			}
			src, name, err := s.openDataSource(source)
			if err != nil {
				return err
			}

			key := cache.Key("data", []string{name, "tool", provider + "/" + capability}, "", "")
			var found data.Tool
			entry, err := s.dataCached(key, &found, func() (any, error) {
				return data.Lookup(cmd.Context(), src, provider, capability, timeout)
			})
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data:    found,
				Meta:    dataMeta(name, 1, entry, 1),
				Columns: toolColumns,
				Rows:    toolRows([]data.Tool{found}),
				Records: toolRecords([]data.Tool{found}),
				Text:    func(w io.Writer) error { return writeToolDetailText(w, found, s.dataMaxCredits()) },
			})
		},
	}

	run := &cobra.Command{
		Use:   "run <provider/capability>",
		Short: "Run one tool and print its answer (billed)",
		Long: `run executes one tool and prints the provider's answer, verbatim.

It is the one data command that costs money. The tool's price is read from the
catalogue first, and a tool listed above --max-credits is refused there, with
nothing spent; raise it for one run once whoever pays has agreed to the price.
Tools that look up a person's email address or phone number are refused.

Options are the tool's inputs, by the names "text data tool" lists. A value is
read as JSON when it parses as JSON — a number, true, a ["list"] — and as text
otherwise; quote a string that looks like a number: --opt zip='"01234"'.

The answer is cached on the tool and its options for 24h, so reading it again
is free. --refresh buys a fresh one. A run that times out or fails upstream may
still have been billed: its error names the request id it went out under, and
rerunning with --request-id <that id> is answered from the first attempt
instead of being billed twice.

Examples:
  text data run fullenrich/people/lookup --opt person_name="Ada Example" --opt company_domain=example.com
  text data run fiscal-ai/financials/income-statement --opt ticker=MSFT --output text
  text data run fred/series/search --options '{"search_text":"unemployment rate","limit":5}' --output ndjson
  text data run similarweb/web/traffic --opt domain=example.com --max-credits 110`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			flags := cmd.Flags()
			provider, capability, err := data.ParseID(args[0])
			if err != nil {
				return err
			}
			rawJSON, _ := flags.GetString("options")
			pairs, _ := flags.GetStringArray("opt")
			options, err := parseToolOptions(rawJSON, pairs)
			if err != nil {
				return err
			}
			requestID, _ := flags.GetString("request-id")

			maxCredits := s.dataMaxCredits()
			if flags.Changed("max-credits") {
				maxCredits, _ = flags.GetInt("max-credits")
				if maxCredits < 0 {
					return errs.New(errs.CodeInvalidArgs, "--max-credits cannot be negative").
						WithHint("Use 0 to allow only free tools.")
				}
			}
			capCredits, capped, err := data.CapFromEnv()
			if err != nil {
				return err
			}
			budget := data.Budget{
				MaxCredits: maxCredits,
				Explicit:   flags.Changed("max-credits"),
				Cap:        capCredits,
				Capped:     capped,
			}

			src, name, err := s.openDataSource(source)
			if err != nil {
				return err
			}

			// The options are part of the key as canonical JSON — map keys
			// sorted at every level — so the same call reaches the same entry
			// however its flags were ordered.
			canonical, err := json.Marshal(options)
			if err != nil {
				return errs.Newf(errs.CodeInvalidArgs, "encode options: %s", err.Error())
			}
			key := cache.Key("data", []string{name, "run", provider + "/" + capability, string(canonical)}, "", "")

			var res data.Result
			entry, err := s.dataCached(key, &res, func() (any, error) {
				return data.Run(cmd.Context(), src, data.Call{
					Provider:   provider,
					Capability: capability,
					Options:    options,
					RequestID:  requestID,
					Timeout:    timeout,
				}, budget)
			})
			if err != nil {
				return err
			}

			records := data.RecordsOf(&res)
			ndjson := make([]any, 0, len(records))
			for _, r := range records {
				ndjson = append(ndjson, r)
			}
			// Two calls on a miss: the free lookup the price is read from, and
			// the run.
			return emitResult(cmd, emitOpts{
				Data:    res,
				Meta:    dataMeta(name, len(records), entry, 2),
				Records: ndjson,
				Text:    func(w io.Writer) error { return writeRunText(w, res) },
			})
		},
	}
	run.Flags().StringArray("opt", nil, "one option as key=value, read as JSON when it parses (repeatable)")
	run.Flags().String("options", "", "all options as one JSON object; --opt values override its keys")
	run.Flags().Int("max-credits", config.DefaultDataMaxCredits, "most this run may cost (default: config data.max_credits)")
	run.Flags().String("request-id", "", "idempotency key; reuse the one a failed run's error names to avoid paying twice")

	pf := c.PersistentFlags()
	pf.StringVar(&source, "source", "", "data source: "+strings.Join(data.Names(), "|"))
	pf.DurationVar(&timeout, "timeout", data.DefaultTimeout, "per-request timeout")

	c.AddCommand(find, tool, run)
	return c
}

// openDataSource resolves --source, then the config, then the registry's
// default, and injects the credential and endpoint. Same contract as
// openResearchSource: the resolved name is the cache-key component and what
// meta.provider reports, so it comes back with the source.
func (s *State) openDataSource(flag string) (data.Source, string, error) {
	name := strings.TrimSpace(flag)
	if name == "" && s.Cfg != nil {
		name = strings.TrimSpace(s.Cfg.Data.Source)
	}
	if name == "" {
		name = data.Default()
	}
	if name == "" {
		return nil, "", errs.New(errs.CodeProviderUnavailable, "no data source is registered").
			WithHint("This is a build problem, not a configuration one.")
	}
	src, err := data.Open(name)
	if err != nil {
		return nil, "", err
	}
	if api, ok := src.(data.APIConfigurer); ok {
		var configured, base string
		if s.Cfg != nil {
			configured = s.Cfg.Firecrawl.APIKey
			base = s.Cfg.Firecrawl.BaseURL
		}
		api.SetAPIKey(firecrawl.ResolveKey(configured))
		api.SetBaseURL(base)
	}
	return src, name, nil
}

// dataMaxCredits is the ceiling a run gets without --max-credits.
func (s *State) dataMaxCredits() int {
	if s.Cfg == nil {
		return data.DefaultMaxCredits
	}
	return s.Cfg.Data.MaxCredits
}

// dataCached is the read-through cache of the three subcommands — the shape of
// researchCached, with the data TTL. Errors are never cached: a run that found
// nobody is asked again, and a per-record tool charges nothing for that.
func (s *State) dataCached(key string, out any, call func() (any, error)) (*cache.Entry, error) {
	if s.Cache != nil && !s.NoCache && !s.Refresh {
		if entry, err := s.Cache.Get(key); err == nil && entry != nil {
			if err := json.Unmarshal(entry.Payload, out); err == nil {
				return entry, nil
			}
			// A corrupt payload is a miss, not a failure.
		}
	}

	result, err := call()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, errs.Newf(errs.CodeGeneric, "encode result: %s", err.Error())
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return nil, errs.Newf(errs.CodeGeneric, "decode result: %s", err.Error())
	}
	if s.Cache != nil && !s.NoCache {
		ttl := 24 * time.Hour
		if s.Cfg != nil {
			ttl = s.Cfg.DataTTL()
		}
		_ = s.Cache.Put(key, payload, s.TTLFor(ttl))
	}
	return nil, nil
}

func dataMeta(name string, documents int, entry *cache.Entry, calls int) output.Meta {
	meta := output.Meta{Provider: name, Documents: documents}
	if entry != nil {
		meta.Cached = true
		meta.CachedAt = entry.CachedAt.Format(time.RFC3339)
		sec := int(entry.Remaining().Seconds())
		meta.TTLRemainingSec = &sec
		return meta
	}
	meta.APICalls = calls
	return meta
}

// parseToolOptions merges --options and --opt into one options object.
//
// A --opt value is read as JSON when it parses as JSON, because the options a
// tool takes are typed — a limit is a number, a list of domains is a list —
// and as text otherwise, because most values are names and domains that
// nobody should have to quote twice.
func parseToolOptions(rawJSON string, pairs []string) (map[string]any, error) {
	options := map[string]any{}
	if v := strings.TrimSpace(rawJSON); v != "" {
		dec := json.NewDecoder(strings.NewReader(v))
		dec.UseNumber()
		if err := dec.Decode(&options); err != nil {
			return nil, errs.Newf(errs.CodeInvalidArgs, "--options is not a JSON object: %s", err.Error()).
				WithHint(`Pass an object, e.g. --options '{"ticker":"MSFT"}'.`)
		}
	}
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, errs.Newf(errs.CodeInvalidArgs, "--opt %q is not key=value", pair).
				WithHint(`E.g. --opt ticker=MSFT, or --opt person_name="Ada Example".`)
		}
		options[key] = optionValue(value)
	}
	return options, nil
}

// optionValue reads one --opt value: JSON when it parses as a complete JSON
// value, the text itself when it does not.
func optionValue(raw string) any {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err == nil && !dec.More() {
		return v
	}
	return raw
}

var toolColumns = []string{"id", "credits", "per_record", "name", "refused"}

func toolRows(tools []data.Tool) []output.Row {
	rows := []output.Row{}
	for _, t := range tools {
		rows = append(rows, output.Row{
			"id":         t.ID,
			"credits":    t.Credits,
			"per_record": t.PerRecord,
			"name":       t.Name,
			"refused":    t.Refused,
		})
	}
	return rows
}

func toolRecords(tools []data.Tool) []any {
	records := make([]any, 0, len(tools))
	for _, t := range tools {
		records = append(records, t)
	}
	return records
}

// price renders a tool's listed price.
func price(t data.Tool) string {
	switch {
	case t.Credits == 0:
		return "free"
	case t.PerRecord:
		return fmt.Sprintf("%d credits per record", t.Credits)
	default:
		return fmt.Sprintf("%d credits", t.Credits)
	}
}

// needs renders the inputs a tool insists on: "one of a, b; and c".
func needs(groups [][]string) string {
	parts := make([]string, 0, len(groups))
	for _, g := range groups {
		if len(g) == 1 {
			parts = append(parts, g[0])
			continue
		}
		parts = append(parts, "one of "+strings.Join(g, ", "))
	}
	return strings.Join(parts, "; and ")
}

func writeToolsText(w io.Writer, tools []data.Tool) error {
	if len(tools) == 0 {
		fmt.Fprintln(w, "No tool matches. Describe the data rather than naming the person or company.")
		return nil
	}
	for i, t := range tools {
		if i > 0 {
			fmt.Fprintln(w)
		}
		head := t.ID + "  ·  " + price(t)
		if t.Name != "" {
			head += "  ·  " + t.Name
		}
		fmt.Fprintln(w, head)
		if t.Description != "" {
			fmt.Fprintln(w, wrapText(t.Description, textWrapWidth))
		}
		if n := needs(t.RequiresOneOf); n != "" {
			fmt.Fprintln(w, "needs: "+n)
		}
		if t.Refused != "" {
			fmt.Fprintln(w, "refused: "+t.Refused)
		}
	}
	return nil
}

func writeToolDetailText(w io.Writer, t data.Tool, ceiling int) error {
	if err := writeToolsText(w, []data.Tool{t}); err != nil {
		return err
	}
	if t.Returns != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, wrapText("returns: "+t.Returns, textWrapWidth))
	}
	if len(t.Options) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Options")
		for _, o := range t.Options {
			fmt.Fprintln(w, "  "+optionLine(o))
		}
	}
	if t.Credits > ceiling && t.Refused == "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Above the %d-credit ceiling: a run needs --max-credits %d.\n", ceiling, t.Credits)
	}
	return nil
}

// optionLine renders one option: `limit (integer, default 2, max 6) — Records per page.`
func optionLine(o map[string]any) string {
	name, _ := o["name"].(string)
	var facts []string
	if typ, _ := o["type"].(string); typ != "" {
		facts = append(facts, typ)
	}
	for _, k := range []string{"default", "min", "max"} {
		if v, ok := o[k]; ok && v != nil {
			b, _ := json.Marshal(v)
			facts = append(facts, k+" "+string(b))
		}
	}
	if list, ok := o["enum"].([]any); ok && len(list) > 0 {
		b, _ := json.Marshal(list)
		facts = append(facts, "one of "+string(b))
	}
	line := name
	if len(facts) > 0 {
		line += " (" + strings.Join(facts, ", ") + ")"
	}
	if about, _ := o["about"].(string); strings.TrimSpace(about) != "" {
		line += " — " + strings.Join(strings.Fields(about), " ")
	}
	return line
}

func writeRunText(w io.Writer, res data.Result) error {
	head := []string{res.Tool, fmt.Sprintf("%d credits", res.Credits)}
	if res.Records != nil {
		head = append(head, fmt.Sprintf("%d records", *res.Records))
	}
	head = append(head, "request "+res.RequestID)
	fmt.Fprintln(w, strings.Join(head, "  ·  "))
	fmt.Fprintln(w)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, res.Data, "", "  "); err != nil {
		_, err := w.Write(res.Data)
		return err
	}
	fmt.Fprintln(w, pretty.String())
	return nil
}
