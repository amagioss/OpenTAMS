package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/internal/tamsctl"
)

// listEnvelope is the structured (json/yaml) shape for paginated list output:
// the items array plus the cursor for the next page.
type listEnvelope struct {
	Items         json.RawMessage `json:"items"`
	NextPageToken string          `json:"nextPageToken,omitempty"`
}

// pageFetcher fetches one page of a listing for the given page token.
type pageFetcher func(pageToken string) (*tamsctl.Page, error)

// addListFlags registers the pagination flags that runList consumes. Kept
// next to runList (the sole consumer) so the flag names can't drift between
// registration and use, and so every list command shares one definition.
func addListFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("page-token", "", "pagination cursor (from a prior page)")
	f.Bool("all", false, "fetch all pages and concatenate results")
	f.Int("max-pages", 10000, "safety cap on pages fetched by --all (<=0 = unlimited)")
}

// runList drives a paginated listing: it honours --page-token, --all,
// --max-pages, the output format, and --quiet, then renders the result.
func runList(cmd *cobra.Command, fetch pageFetcher) error {
	all, _ := cmd.Flags().GetBool("all")
	startToken, _ := cmd.Flags().GetString("page-token")

	page, err := fetch(startToken)
	if err != nil {
		return err
	}
	if all {
		maxPages, _ := cmd.Flags().GetInt("max-pages")
		page, err = collectRemaining(page, fetch, maxPages)
		if err != nil {
			return err
		}
	}
	return emitPage(cmd, page)
}

// collectRemaining follows next-page cursors from the first page onward,
// concatenating every item into a single page with an empty NextToken. The
// first page counts toward maxPages. It guards against a misbehaving server:
// a cursor that does not advance is rejected rather than looped on forever, and
// the page count is capped at maxPages (<= 0 means unlimited).
func collectRemaining(first *tamsctl.Page, fetch pageFetcher, maxPages int) (*tamsctl.Page, error) {
	// Start non-nil so an all-empty result marshals to [] (matching the
	// single-page path), never null.
	all := []json.RawMessage{}
	items, err := decodeItems(first.Items)
	if err != nil {
		return nil, err
	}
	all = append(all, items...)
	pages := 1
	prevToken := ""
	token := first.NextToken
	for token != "" {
		if token == prevToken {
			return nil, fmt.Errorf("server returned a non-advancing pagination cursor %q; aborting --all", token)
		}
		if maxPages > 0 && pages >= maxPages {
			return nil, fmt.Errorf("--all stopped after --max-pages=%d; narrow the query or resume with --page-token %s", maxPages, token)
		}
		page, ferr := fetch(token)
		if ferr != nil {
			return nil, ferr
		}
		items, derr := decodeItems(page.Items)
		if derr != nil {
			return nil, derr
		}
		all = append(all, items...)
		pages++
		prevToken, token = token, page.NextToken
	}
	merged, err := json.Marshal(all)
	if err != nil {
		return nil, fmt.Errorf("merge pages: %w", err)
	}
	return &tamsctl.Page{Items: merged}, nil
}

func decodeItems(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse list items: %w", err)
	}
	return items, nil
}

// emitPage renders a page. For table output the next-page hint goes to stderr
// (unless --quiet); for json/yaml the cursor is embedded in the stdout envelope.
func emitPage(cmd *cobra.Command, page *tamsctl.Page) error {
	format, _ := cmd.Flags().GetString("output")
	items := itemsOrEmpty(page.Items)

	if format == "table" {
		if err := render(cmd.OutOrStdout(), items, "table"); err != nil {
			return err
		}
		quiet, _ := cmd.Flags().GetBool("quiet")
		if page.NextToken != "" && !quiet {
			return printNextHint(cmd, page.NextToken)
		}
		return nil
	}

	env := listEnvelope{Items: items, NextPageToken: page.NextToken}
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}
	return render(cmd.OutOrStdout(), raw, format)
}

func itemsOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
}

// printNextHint writes a human next-page hint to stderr.
func printNextHint(cmd *cobra.Command, token string) error {
	_, err := fmt.Fprintf(cmd.ErrOrStderr(),
		"\nMore results available (next-page token: %s)\n  %s\n",
		token, nextCommand(token))
	return err
}

// nextCommand reconstructs the current invocation with --page-token set to the
// given cursor (replacing any existing --page-token).
func nextCommand(token string) string {
	args := os.Args
	out := []string{filepath.Base(args[0])}
	skip := false
	for _, a := range args[1:] {
		switch {
		case skip:
			skip = false
		case a == "--page-token":
			skip = true
		case strings.HasPrefix(a, "--page-token="):
			// drop
		case a == "--all":
			// dropping --all so the suggestion fetches just the next page
		default:
			out = append(out, a)
		}
	}
	out = append(out, "--page-token", token)
	return strings.Join(out, " ")
}
