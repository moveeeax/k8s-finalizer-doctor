package scan

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// WriteJSON prints the machine-readable report.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteTable prints the human report: one row per blocker, then the exact
// commands for everything classified as safe to strip.
func WriteTable(w io.Writer, r Report) error {
	if !r.Stuck() {
		_, err := fmt.Fprintf(w, "nothing terminating: %d object(s) checked, no finalizer blockers\n", r.Scanned)
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "OBJECT\tFINALIZER\tVERDICT\tACTION\tOWNER\tMATCHED-BY")
	for _, f := range r.Findings {
		name := f.Kind + "/" + f.Name
		if f.Namespace != "" {
			name = f.Namespace + " " + name
		}
		for _, b := range f.Blockers {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", name, b.Finalizer, b.Verdict, b.Verdict.Action(), b.Owner, b.MatchedBy)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	var details strings.Builder
	for _, f := range r.Findings {
		for _, b := range f.Blockers {
			if b.Warning == "" && b.Remediation == "" {
				continue
			}
			name := f.Kind + "/" + f.Name
			if f.Namespace != "" {
				name = f.Namespace + " " + name
			}
			fmt.Fprintf(&details, "\n%s  %s\n  %s\n", name, b.Finalizer, b.Reason)
			if b.Warning != "" {
				fmt.Fprintf(&details, "  warning: stripping %s\n", b.Warning)
			}
			if b.Remediation != "" {
				fmt.Fprintf(&details, "  %s\n", b.Remediation)
			}
		}
	}
	if details.Len() > 0 {
		if _, err := io.WriteString(w, details.String()); err != nil {
			return err
		}
	}

	counts := map[Verdict]int{}
	for _, f := range r.Findings {
		for _, b := range f.Blockers {
			counts[b.Verdict]++
		}
	}
	_, err := fmt.Fprintf(w, "\n%d object(s) stuck: %d safe to strip, %d blocked on a broken controller, %d still waiting\n",
		len(r.Findings), counts[OwnerMissing], counts[OwnerUnhealthy], counts[OwnerHealthy]+counts[OwnerBuiltin])
	return err
}
