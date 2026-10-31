package metric_test

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The /metrics page is a published interface: the chart's alerting rules and
// the dashboards built on it name its families, its labels and its types, and
// none of them would notice a rename until a rule went silently Inactive. So
// the page is pinned to a snapshot of every concourse_ family it carries after
// every production emit site has fired once: name, type, help, label names and
// histogram buckets. Values are not pinned; they depend on timing.
//
// driveEveryMetric (snapshot_script_test.go) fires the emit sites and
// servePrometheus (snapshot_serve_test.go) opens the listener; this file and
// testdata/metric_families.txt do not change when the plumbing behind them
// does. Set METRIC_SNAPSHOT_UPDATE=1 to rewrite the snapshot deliberately.
const snapshotPath = "testdata/metric_families.txt"

var _ = Describe("the /metrics page", func() {
	It("publishes exactly the snapshotted families, labels, types and help", func() {
		useEmptyTestDB()

		address := servePrometheus(map[string]string{
			// Attributes become constant labels on every family, and their
			// names are sanitized on the way in.
			"cluster":       "snapshot",
			"invalid-label": "sanitized",
		})

		driveEveryMetric()

		page := func() string { return familySnapshot(scrapeMetrics(address)) }

		if os.Getenv("METRIC_SNAPSHOT_UPDATE") != "" {
			time.Sleep(2 * time.Second)
			Expect(os.MkdirAll(filepath.Dir(snapshotPath), 0o755)).To(Succeed())
			Expect(os.WriteFile(snapshotPath, []byte(page()), 0o644)).To(Succeed())
		}

		golden, err := os.ReadFile(snapshotPath)
		Expect(err).NotTo(HaveOccurred())

		Eventually(page, 10*time.Second, 100*time.Millisecond).Should(Equal(string(golden)))
	})
})

func scrapeMetrics(address string) string {
	GinkgoHelper()

	res, err := http.Get(fmt.Sprintf("http://%s/metrics", address))
	if err != nil {
		return ""
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	Expect(err).NotTo(HaveOccurred())

	return string(body)
}

type family struct {
	kind    string
	help    string
	labels  map[string]bool
	buckets []string
}

// familySnapshot reduces a text-format page to its concourse_ families. It
// reads the exposition format directly rather than through a parser library
// so that what is pinned is what a scraper is handed.
func familySnapshot(page string) string {
	families := map[string]*family{}
	get := func(name string) *family {
		if families[name] == nil {
			families[name] = &family{labels: map[string]bool{}}
		}
		return families[name]
	}

	scanner := bufio.NewScanner(strings.NewReader(page))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "# HELP "); ok {
			name, help, _ := strings.Cut(rest, " ")
			if strings.HasPrefix(name, "concourse_") {
				get(name).help = help
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			name, kind, _ := strings.Cut(rest, " ")
			if strings.HasPrefix(name, "concourse_") {
				get(name).kind = kind
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "concourse_") {
			continue
		}

		name, labels := sampleNameAndLabels(line)
		f := families[name]
		if f == nil {
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				if base, ok := strings.CutSuffix(name, suffix); ok && families[base] != nil {
					f = families[base]
				}
			}
		}
		if f == nil {
			f = get(name)
		}
		for label, value := range labels {
			if label == "le" && strings.HasSuffix(name, "_bucket") {
				if !slices.Contains(f.buckets, value) {
					f.buckets = append(f.buckets, value)
				}
				continue
			}
			f.labels[label] = true
		}
	}

	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	slices.Sort(names)

	var out strings.Builder
	for _, name := range names {
		f := families[name]
		fmt.Fprintf(&out, "%s %s\n", name, f.kind)
		fmt.Fprintf(&out, "  help: %s\n", f.help)
		labels := make([]string, 0, len(f.labels))
		for label := range f.labels {
			labels = append(labels, label)
		}
		slices.Sort(labels)
		fmt.Fprintf(&out, "  labels: %s\n", strings.Join(labels, " "))
		if len(f.buckets) > 0 {
			fmt.Fprintf(&out, "  buckets: %s\n", strings.Join(f.buckets, " "))
		}
	}
	return out.String()
}

// sampleNameAndLabels splits `name{a="x",b="y\"z"} 1` into its name and
// labels, honouring the format's escapes inside label values.
func sampleNameAndLabels(line string) (string, map[string]string) {
	labels := map[string]string{}

	brace := strings.IndexByte(line, '{')
	if brace < 0 {
		name, _, _ := strings.Cut(line, " ")
		return name, labels
	}

	name := line[:brace]
	rest := line[brace+1:]
	for len(rest) > 0 && rest[0] != '}' {
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			break
		}
		label := rest[:eq]
		rest = rest[eq+2:] // skip ="
		var value strings.Builder
		for i := 0; i < len(rest); i++ {
			if rest[i] == '\\' && i+1 < len(rest) {
				value.WriteByte(rest[i+1])
				i++
				continue
			}
			if rest[i] == '"' {
				rest = rest[i+1:]
				break
			}
			value.WriteByte(rest[i])
		}
		labels[label] = value.String()
		rest = strings.TrimPrefix(rest, ",")
	}
	return name, labels
}
