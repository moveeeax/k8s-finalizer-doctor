// Command k8s-finalizer-doctor explains why objects are stuck Terminating.
//
// Exit codes: 0 nothing stuck, 1 at least one object blocked on a finalizer,
// 2 the tool itself failed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/moveeeax/k8s-finalizer-doctor/internal/kube"
	"github.com/moveeeax/k8s-finalizer-doctor/internal/scan"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `k8s-finalizer-doctor - name the finalizer that is holding a delete, and its owner

Usage:
  k8s-finalizer-doctor [flags]

Flags:
  -A, --all-namespaces   scan every namespace (default)
  -n, --namespace ns     restrict to one namespace (cluster-scoped objects are still shown)
  -o, --output form      table (default) or json
      --snapshot file    read a snapshot instead of a cluster, for CI and postmortems
      --dump file        write the collected cluster state to file as a snapshot
      --kubeconfig path  explicit kubeconfig
      --context name     kubeconfig context to use
      --version          print version and exit
`

type options struct {
	allNamespaces bool
	namespace     string
	output        string
	snapshot      string
	dump          string
	kubeconfig    string
	kubeContext   string
	showVersion   bool
}

func parseFlags(args []string, stderr *os.File) (options, error) {
	var o options
	fs := flag.NewFlagSet("k8s-finalizer-doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	fs.BoolVar(&o.allNamespaces, "all-namespaces", false, "")
	fs.BoolVar(&o.allNamespaces, "A", false, "")
	fs.StringVar(&o.namespace, "namespace", "", "")
	fs.StringVar(&o.namespace, "n", "", "")
	fs.StringVar(&o.output, "output", "table", "")
	fs.StringVar(&o.output, "o", "table", "")
	fs.StringVar(&o.snapshot, "snapshot", "", "")
	fs.StringVar(&o.dump, "dump", "", "")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "")
	fs.StringVar(&o.kubeContext, "context", "", "")
	fs.BoolVar(&o.showVersion, "version", false, "")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.allNamespaces {
		o.namespace = ""
	}
	if o.output != "table" && o.output != "json" {
		return o, fmt.Errorf("unknown --output %q, want table or json", o.output)
	}
	return o, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := parseFlags(args, os.Stderr)
	if err != nil {
		return 2
	}
	if opts.showVersion {
		fmt.Println("k8s-finalizer-doctor " + version)
		return 0
	}

	var snap scan.Snapshot
	if opts.snapshot != "" {
		snap, err = scan.LoadSnapshot(opts.snapshot)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
		snap = scan.FilterNamespace(snap, opts.namespace)
	} else {
		client, err := kube.New(opts.kubeconfig, opts.kubeContext)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
		var warnings []error
		snap, warnings = client.Collect(context.Background(), opts.namespace)
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
	}

	if opts.dump != "" {
		if err := scan.SaveSnapshot(opts.dump, snap); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
	}

	report := scan.Resolve(snap)
	if opts.output == "json" {
		err = scan.WriteJSON(os.Stdout, report)
	} else {
		err = scan.WriteTable(os.Stdout, report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if report.Stuck() {
		return 1
	}
	return 0
}
