// Package plan decides how to move a running platform from one release to another: which checks
// must pass before anything is touched, the ordered steps of the upgrade, and the steps that put
// the platform back if one of them fails. It has no Kubernetes dependency; the controller executes
// the steps and the upgrade lab simulates them.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
)

// Severity of a finding.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Finding is one problem with the application config.
type Finding struct {
	Severity Severity `json:"severity"`
	Key      string   `json:"key"`
	Release  string   `json:"release"`
	Message  string   `json:"message"`
}

// MigrateConfig carries a config through every release on the path, applying renames, removals and
// defaults, and checks it against each release's rules. The input map is not modified.
func MigrateConfig(cat *release.Catalog, cfg map[string]string, path []string) (map[string]string, []Finding) {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	var fs []Finding
	for _, ver := range path {
		r, _ := cat.Get(ver)
		rules := r.Config
		for _, old := range sortedKeys(rules.Renames) {
			nu := rules.Renames[old]
			v, ok := out[old]
			if !ok {
				continue
			}
			if existing, both := out[nu]; both && existing != v {
				fs = append(fs, Finding{Warning, old, ver, fmt.Sprintf("%s renamed to %s, which is already set; keeping %s=%s", old, nu, nu, existing)})
			} else {
				out[nu] = v
			}
			delete(out, old)
		}
		for _, k := range rules.Removed {
			if _, ok := out[k]; ok {
				fs = append(fs, Finding{Warning, k, ver, fmt.Sprintf("%s is removed in %s and will be dropped", k, ver)})
				delete(out, k)
			}
		}
		for _, k := range sortedKeys(rules.Defaults) {
			if _, ok := out[k]; !ok {
				out[k] = rules.Defaults[k]
			}
		}
		known := map[string]bool{}
		for _, k := range rules.Known {
			known[k] = true
		}
		for _, k := range sortedKeys(out) {
			if !known[k] {
				msg := fmt.Sprintf("%s is not a setting of %s", k, ver)
				if s := closest(k, rules.Known); s != "" {
					msg += fmt.Sprintf("; did you mean %s?", s)
				}
				fs = append(fs, Finding{Error, k, ver, msg})
			}
		}
		for _, k := range rules.Required {
			if _, ok := out[k]; !ok {
				fs = append(fs, Finding{Error, k, ver, fmt.Sprintf("%s is required by %s", k, ver)})
			}
		}
		for _, k := range sortedKeys(rules.Values) {
			v, ok := out[k]
			if ok && !contains(rules.Values[k], v) {
				fs = append(fs, Finding{Error, k, ver, fmt.Sprintf("%s=%s is not allowed in %s (allowed: %s)", k, v, ver, strings.Join(rules.Values[k], ", "))})
			}
		}
	}
	return out, dedupe(fs)
}

// Policy is how careful an upgrade is.
type Policy struct {
	Preflight             bool    `json:"preflight"`
	BackupBeforeMigration bool    `json:"backupBeforeMigration"`
	AutoRollback          bool    `json:"autoRollback"`
	BackupHeadroom        float64 `json:"backupHeadroom"` // free space needed as a multiple of the database size
}

// DefaultPolicy is what the operator does unless told otherwise.
func DefaultPolicy() Policy {
	return Policy{Preflight: true, BackupBeforeMigration: true, AutoRollback: true, BackupHeadroom: 1.2}
}

// Facts are what the operator observed about the running platform before starting.
type Facts struct {
	ComponentsReady       bool  `json:"componentsReady"`
	DatabaseUsedBytes     int64 `json:"databaseUsedBytes"`
	DatabaseCapacityBytes int64 `json:"databaseCapacityBytes"`
	QueueDepth            int64 `json:"queueDepth"` // messages waiting in the broker
}

// Check is one preflight result.
type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Preflight is the outcome of checking an upgrade before it starts.
type Preflight struct {
	Path     []string          `json:"path"`
	Config   map[string]string `json:"config"`
	Findings []Finding         `json:"findings"`
	Checks   []Check           `json:"checks"`
}

// OK means every check passed.
func (p Preflight) OK() bool {
	for _, c := range p.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Failed lists the names of failed checks.
func (p Preflight) Failed() []string {
	var out []string
	for _, c := range p.Checks {
		if !c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

// NeedsBackup reports whether any release on the path has a migration that cannot be reversed.
func NeedsBackup(cat *release.Catalog, from string, path []string) bool {
	prev, _ := cat.Get(from)
	for _, v := range path {
		r, _ := cat.Get(v)
		if r.Schema != prev.Schema && !r.Reversible {
			return true
		}
		prev = r
	}
	return false
}

func changesMessaging(cat *release.Catalog, from string, path []string) bool {
	prev, _ := cat.Get(from)
	for _, v := range path {
		r, _ := cat.Get(v)
		if r.Messaging != prev.Messaging {
			return true
		}
		prev = r
	}
	return false
}

// RunPreflight checks an upgrade from `from` to `to` without changing anything.
func RunPreflight(cat *release.Catalog, from, to string, cfg map[string]string, f Facts, p Policy) Preflight {
	pf := Preflight{}
	path, err := cat.Path(from, to)
	if err != nil {
		pf.Checks = append(pf.Checks, Check{"path", false, err.Error()})
		return pf
	}
	pf.Path = path
	pf.Checks = append(pf.Checks, Check{"path", true, fmt.Sprintf("%s → %s", from, strings.Join(path, " → "))})
	pf.Config, pf.Findings = MigrateConfig(cat, cfg, path)
	var errs []string
	for _, fd := range pf.Findings {
		if fd.Severity == Error {
			errs = append(errs, fd.Message)
		}
	}
	if len(errs) > 0 {
		pf.Checks = append(pf.Checks, Check{"config", false, strings.Join(errs, "; ")})
	} else {
		pf.Checks = append(pf.Checks, Check{"config", true, fmt.Sprintf("%d keys valid for %s", len(pf.Config), to)})
	}
	if f.ComponentsReady {
		pf.Checks = append(pf.Checks, Check{"health", true, "all components ready"})
	} else {
		pf.Checks = append(pf.Checks, Check{"health", false, "not all components are ready; fix the running platform before upgrading"})
	}
	if p.BackupBeforeMigration && NeedsBackup(cat, from, path) {
		need := int64(float64(f.DatabaseUsedBytes) * p.BackupHeadroom)
		free := f.DatabaseCapacityBytes - f.DatabaseUsedBytes
		if free >= need {
			pf.Checks = append(pf.Checks, Check{"backup-space", true, fmt.Sprintf("%s free for a %s backup", gib(free), gib(f.DatabaseUsedBytes))})
		} else {
			pf.Checks = append(pf.Checks, Check{"backup-space", false, fmt.Sprintf("backup needs %s free, %s available; grow the database volume", gib(need), gib(free))})
		}
	}
	if changesMessaging(cat, from, path) {
		if f.QueueDepth == 0 {
			pf.Checks = append(pf.Checks, Check{"queues-drained", true, "broker queues are empty"})
		} else {
			pf.Checks = append(pf.Checks, Check{"queues-drained", false, fmt.Sprintf("%d messages still queued; the broker upgrade would lose them", f.QueueDepth)})
		}
	}
	return pf
}

// Kind of step.
type Kind string

const (
	Backup        Kind = "Backup"        // dump the database to the backup volume
	Quiesce       Kind = "Quiesce"       // stop writers before a migration that cannot run online
	Migrate       Kind = "Migrate"       // run the schema migration job
	Messaging     Kind = "Messaging"     // roll the message broker
	Core          Kind = "Core"          // roll the core service (and bring writers back)
	Gateway       Kind = "Gateway"       // roll the API gateway
	Verify        Kind = "Verify"        // all components healthy on the new release
	RestoreBackup Kind = "RestoreBackup" // put the database back from the backup
	MigrateDown   Kind = "MigrateDown"   // reverse a reversible migration
	ManualRestore Kind = "ManualRestore" // nothing safe left to do automatically
)

// Step is one unit of work. Version is the release the step moves towards.
type Step struct {
	Kind    Kind   `json:"kind"`
	Version string `json:"version"`
	Schema  int    `json:"schema,omitempty"`
}

func (s Step) String() string { return fmt.Sprintf("%s(%s)", s.Kind, s.Version) }

// Build lays out the upgrade steps for a path, one release at a time, so every hop ends verified.
func Build(cat *release.Catalog, from string, path []string, p Policy) []Step {
	var steps []Step
	prev, _ := cat.Get(from)
	for _, v := range path {
		r, _ := cat.Get(v)
		if r.Schema != prev.Schema {
			if !r.Reversible {
				if p.BackupBeforeMigration {
					steps = append(steps, Step{Kind: Backup, Version: v, Schema: prev.Schema})
				}
				steps = append(steps, Step{Kind: Quiesce, Version: v})
			}
			steps = append(steps, Step{Kind: Migrate, Version: v, Schema: r.Schema})
		}
		if r.Messaging != prev.Messaging {
			steps = append(steps, Step{Kind: Messaging, Version: v})
		}
		steps = append(steps, Step{Kind: Core, Version: v}, Step{Kind: Gateway, Version: v}, Step{Kind: Verify, Version: v})
		prev = r
	}
	return steps
}

// Rollback returns the steps that put the platform back on the last release that was verified
// before the failure. Applications are reverted first so nothing writes to a schema it does not
// know; then the schema is reversed, or restored from the backup taken for this hop. If neither
// is possible the plan ends in ManualRestore.
func Rollback(cat *release.Catalog, from string, steps []Step, failed int) (target string, out []Step) {
	target = from
	for i := 0; i < failed && i < len(steps); i++ {
		if steps[i].Kind == Verify {
			target = steps[i].Version
		}
	}
	base, _ := cat.Get(target)
	touched := map[Kind]bool{}
	migrated := false
	backedUp := false
	var hop string
	for i := 0; i <= failed && i < len(steps); i++ {
		s := steps[i]
		if release.Compare(s.Version, target) <= 0 {
			continue
		}
		hop = s.Version
		touched[s.Kind] = true
		switch s.Kind {
		case Migrate:
			migrated = true
		case Backup:
			backedUp = i < failed // a failed backup is no backup
		}
	}
	if hop == "" {
		return target, nil
	}
	if touched[Gateway] {
		out = append(out, Step{Kind: Gateway, Version: target})
	}
	if touched[Core] || touched[Quiesce] {
		// Core goes back to the old release but stays stopped until the schema is back too.
		out = append(out, Step{Kind: Quiesce, Version: target})
	}
	if migrated {
		r, _ := cat.Get(hop)
		switch {
		case r.Reversible:
			out = append(out, Step{Kind: MigrateDown, Version: target, Schema: base.Schema})
		case backedUp:
			out = append(out, Step{Kind: RestoreBackup, Version: target, Schema: base.Schema})
		default:
			return target, append(out, Step{Kind: ManualRestore, Version: target, Schema: base.Schema})
		}
	}
	if touched[Core] || touched[Quiesce] {
		out = append(out, Step{Kind: Core, Version: target})
	}
	// The broker is not downgraded: newer brokers serve older clients, and a downgrade would lose
	// its queues. Rolling back leaves it on the new version.
	return target, append(out, Step{Kind: Verify, Version: target})
}

func gib(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func dedupe(fs []Finding) []Finding {
	seen := map[string]bool{}
	var out []Finding
	for _, f := range fs {
		k := string(f.Severity) + "|" + f.Key + "|" + f.Message
		if !seen[k] {
			seen[k] = true
			out = append(out, f)
		}
	}
	return out
}

// closest suggests a known key within edit distance 2, for typos such as retention.day.
func closest(k string, known []string) string {
	best, dist := "", 3
	for _, c := range known {
		if d := levenshtein(k, c); d < dist {
			best, dist = c, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
