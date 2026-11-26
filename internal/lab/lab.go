// Package lab upgrades a simulated fleet of customer sites to one target release under several
// upgrade designs and counts what happens. Each site's luck (its config, its disk, whether its
// queues are drained, which jobs fail) is drawn once per seed and shared by every design, so the
// designs differ only in how they upgrade. The operator designs run the real preflight, plan and
// rollback code from internal/plan against the real release catalog.
package lab

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"sort"

	"sigs.k8s.io/yaml"

	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/plan"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
)

// Config is config/lab.yaml. Every rate in it is an assumption.
type Config struct {
	Fleet struct {
		Sites       int                `json:"sites"`
		Target      string             `json:"target"`
		Start       map[string]float64 `json:"start"`
		DatabaseGiB [2]float64         `json:"database_gib"`
	} `json:"fleet"`
	World struct {
		ConfigTypo      float64 `json:"config_typo"`
		QueueNotDrained float64 `json:"queue_not_drained"`
		LowBackupSpace  float64 `json:"low_backup_space"`
		UnhealthyBefore float64 `json:"unhealthy_before"`
		MigrationFails  struct {
			Irreversible float64 `json:"irreversible"`
			Reversible   float64 `json:"reversible"`
		} `json:"migration_fails"`
		RolloutStalls   float64 `json:"rollout_stalls"`
		OnlineWriteLoss float64 `json:"online_write_loss"`
		RestoreFails    float64 `json:"restore_fails"`
	} `json:"world"`
	People struct {
		SkipsCheck    float64 `json:"skips_check"`
		MissesBackup  float64 `json:"misses_backup"`
		MissesQuiesce float64 `json:"misses_quiesce"`
		MissesRename  float64 `json:"misses_rename"`
	} `json:"people"`
	Minutes struct {
		BackupPerGiB        float64 `json:"backup_per_gib"`
		RestorePerGiB       float64 `json:"restore_per_gib"`
		MigrateIrreversible float64 `json:"migrate_irreversible"`
		MigrateReversible   float64 `json:"migrate_reversible"`
		Rollout             float64 `json:"rollout"`
		Broker              float64 `json:"broker"`
		BrokerOutage        float64 `json:"broker_outage"`
		Verify              float64 `json:"verify"`
		Quiesce             float64 `json:"quiesce"`
		ManualStepOverhead  float64 `json:"manual_step_overhead"`
		Diagnose            float64 `json:"diagnose"`
		ManualRecovery      float64 `json:"manual_recovery"`
	} `json:"minutes"`
	Engineer struct {
		OperatorReview     float64 `json:"operator_review"`
		HelmAttendPerHop   float64 `json:"helm_attend_per_hop"`
		ScriptedPrepare    float64 `json:"scripted_prepare"`
		FixConfig          float64 `json:"fix_config"`
		GrowVolume         float64 `json:"grow_volume"`
		FixHealth          float64 `json:"fix_health"`
		InvestigateFailure float64 `json:"investigate_failure"`
		ManualRecovery     float64 `json:"manual_recovery"`
	} `json:"engineer_minutes"`
	Seeds struct {
		Dev     []uint64 `json:"dev"`
		Heldout []uint64 `json:"heldout"`
	} `json:"seeds"`
}

// LoadConfig reads and checks the lab settings.
func LoadConfig(path string) (*Config, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var c Config
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Fleet.Sites <= 0 || len(c.Fleet.Start) == 0 || c.Fleet.Target == "" {
		return nil, nil, fmt.Errorf("%s: fleet needs sites, start and target", path)
	}
	sum := 0.0
	for _, p := range c.Fleet.Start {
		sum += p
	}
	if math.Abs(sum-1) > 1e-9 {
		return nil, nil, fmt.Errorf("%s: fleet.start shares add up to %.3f, not 1", path, sum)
	}
	return &c, raw, nil
}

// Design is one way of upgrading a site.
type Design string

const (
	Scripted            Design = "scripted"              // today: a person follows the runbook
	HelmAtomic          Design = "helm_atomic"           // helm upgrade --atomic per hop
	Operator            Design = "operator"              // this operator, every safeguard on
	OperatorNoPreflight Design = "operator_no_preflight" // ablation: skip the preflight
	OperatorNoBackup    Design = "operator_no_backup"    // ablation: no backup before migrations
)

// Designs in report order.
var Designs = []Design{Scripted, HelmAtomic, Operator, OperatorNoPreflight, OperatorNoBackup}

// Label for reports.
func (d Design) Label() string {
	return map[Design]string{
		Scripted:            "Scripted runbook (today)",
		HelmAtomic:          "helm upgrade --atomic",
		Operator:            "Operator",
		OperatorNoPreflight: "Operator without preflight",
		OperatorNoBackup:    "Operator without backup",
	}[d]
}

// site is one installation and its luck. It is drawn once and shared by every design.
type site struct {
	from        string
	config      map[string]string
	usedGiB     float64
	lowSpace    bool
	queued      bool
	unhealthy   bool
	typo        bool
	migrateFail map[string]bool    // per release on the path
	stall       map[string][2]bool // per release: core, gateway
	writeLoss   bool               // an online irreversible migration would lose writes
	restoreFail bool               // restoring the backup would fail
	skipsCheck  bool
	missBackup  bool
	missQuiesce bool
	missRename  bool
}

func drawSite(c *Config, cat *release.Catalog, rng *rand.Rand) site {
	s := site{migrateFail: map[string]bool{}, stall: map[string][2]bool{}}
	starts := make([]string, 0, len(c.Fleet.Start))
	for v := range c.Fleet.Start {
		starts = append(starts, v)
	}
	sort.Slice(starts, func(i, j int) bool { return release.Compare(starts[i], starts[j]) < 0 })
	u, acc := rng.Float64(), 0.0
	s.from = starts[len(starts)-1]
	for _, v := range starts {
		acc += c.Fleet.Start[v]
		if u < acc {
			s.from = v
			break
		}
	}
	lo, hi := c.Fleet.DatabaseGiB[0], c.Fleet.DatabaseGiB[1]
	s.usedGiB = lo + rng.Float64()*(hi-lo)
	s.lowSpace = rng.Float64() < c.World.LowBackupSpace
	s.queued = rng.Float64() < c.World.QueueNotDrained
	s.unhealthy = rng.Float64() < c.World.UnhealthyBefore
	s.typo = rng.Float64() < c.World.ConfigTypo
	s.writeLoss = rng.Float64() < c.World.OnlineWriteLoss
	s.restoreFail = rng.Float64() < c.World.RestoreFails
	s.skipsCheck = rng.Float64() < c.People.SkipsCheck
	s.missBackup = rng.Float64() < c.People.MissesBackup
	s.missQuiesce = rng.Float64() < c.People.MissesQuiesce
	s.missRename = rng.Float64() < c.People.MissesRename
	// draw for every release so the draws do not depend on the path
	for _, r := range cat.Releases {
		p := c.World.MigrationFails.Reversible
		if !r.Reversible {
			p = c.World.MigrationFails.Irreversible
		}
		s.migrateFail[r.Version] = rng.Float64() < p
		s.stall[r.Version] = [2]bool{rng.Float64() < c.World.RolloutStalls, rng.Float64() < c.World.RolloutStalls}
	}
	audit := "full"
	if s.typo {
		audit = "ful"
	}
	if release.Compare(s.from, "5.0.0") < 0 {
		s.config = map[string]string{"retention.days": "180", "export.format": "xml", "mq.prefetch": "50", "audit.level": audit}
	} else {
		s.config = map[string]string{"retention.days": "180", "handover.format": "xml", "messaging.prefetch": "50", "audit.level": audit}
	}
	return s
}

// Outcome of upgrading one site under one design.
type Outcome struct {
	Reached       bool    // on the target release at the end
	FirstTry      bool    // reached it with no block and no failure
	Blocked       bool    // the preflight stopped it before any change
	Failed        bool    // a step failed
	CleanRollback bool    // failed, and back on the last verified release with data intact
	Broken        bool    // not serving at the end; needs manual recovery
	DataLoss      bool    // committed data lost
	MessageLoss   bool    // queued messages lost in a broker upgrade
	DowntimeMin   float64 // minutes the platform did not serve
	EngineerMin   float64
	Cause         string // what went wrong first, if anything
	BlockedChecks []string
	FinalVersion  string
	StepsExecuted int
	RollbackPlan  []string
}

// world state while a design runs
type run struct {
	c      *Config
	cat    *release.Catalog
	s      site
	d      Design
	out    Outcome
	path   []string
	queued bool
	health bool
	space  bool
}

// Simulate upgrades one site under one design.
func Simulate(c *Config, cat *release.Catalog, s site, d Design) Outcome {
	r := &run{c: c, cat: cat, s: s, d: d, queued: s.queued, health: !s.unhealthy, space: !s.lowSpace}
	r.out.FinalVersion = s.from
	path, err := cat.Path(s.from, c.Fleet.Target)
	if err != nil || len(path) == 0 {
		r.out.Reached = err == nil
		r.out.FirstTry = r.out.Reached
		return r.out
	}
	r.path = path
	switch d {
	case Operator, OperatorNoPreflight, OperatorNoBackup:
		r.operator(path)
	case HelmAtomic:
		r.helm(path)
	case Scripted:
		r.scripted(path)
	}
	r.out.FirstTry = r.out.Reached && !r.out.Blocked && !r.out.Failed && !r.out.DataLoss && !r.out.MessageLoss
	return r.out
}

func (r *run) policy() plan.Policy {
	p := plan.DefaultPolicy()
	switch r.d {
	case OperatorNoPreflight:
		p.Preflight = false
	case OperatorNoBackup:
		p.BackupBeforeMigration = false
	}
	return p
}

func (r *run) facts() plan.Facts {
	used := int64(r.s.usedGiB * (1 << 30))
	capacity := used * 3
	if !r.space {
		capacity = used + used/2 // half the database size free: not enough for a backup
	}
	q := int64(0)
	if r.queued {
		q = 1200
	}
	return plan.Facts{ComponentsReady: r.health, DatabaseUsedBytes: used, DatabaseCapacityBytes: capacity, QueueDepth: q}
}

func (r *run) operator(path []string) {
	e := r.c.Engineer
	pol := r.policy()
	r.out.EngineerMin += e.OperatorReview
	if pol.Preflight {
		for attempt := 0; attempt < 5; attempt++ {
			pf := plan.RunPreflight(r.cat, r.s.from, r.c.Fleet.Target, r.s.config, r.facts(), pol)
			if pf.OK() {
				break
			}
			r.out.Blocked = true
			for _, name := range pf.Failed() {
				r.out.BlockedChecks = appendOnce(r.out.BlockedChecks, name)
				switch name {
				case "config":
					r.s.config, r.out.EngineerMin = fixTypo(r.s.config), r.out.EngineerMin+e.FixConfig
				case "backup-space":
					r.space, r.out.EngineerMin = true, r.out.EngineerMin+e.GrowVolume
				case "health":
					r.health, r.out.EngineerMin = true, r.out.EngineerMin+e.FixHealth
				case "queues-drained":
					r.queued = false // consumers drain the queue; the operator retries on its own
				}
			}
		}
	}
	steps := plan.Build(r.cat, r.s.from, path, pol)
	verified := r.s.from
	down := false
	for i, st := range steps {
		r.out.StepsExecuted++
		fail := r.stepFails(st, verified)
		if !fail {
			down = r.advance(st, down, false)
			if st.Kind == plan.Verify {
				verified = st.Version
				r.out.FinalVersion = verified
			}
			continue
		}
		// automatic rollback with the real planner
		r.out.Failed = true
		target, rb := plan.Rollback(r.cat, r.s.from, steps, i)
		for _, x := range rb {
			r.out.RollbackPlan = append(r.out.RollbackPlan, x.String())
		}
		r.out.EngineerMin += e.InvestigateFailure
		manual := slices.ContainsFunc(rb, func(x plan.Step) bool { return x.Kind == plan.ManualRestore })
		rbMin := 0.0
		for _, x := range rb {
			rbMin += r.duration(x, true)
		}
		if manual || r.restoreFails(rb) {
			r.broken(down)
			return
		}
		if down || st.Kind == plan.Migrate {
			r.out.DowntimeMin += rbMin
		}
		r.out.CleanRollback = true
		r.out.FinalVersion = target
		return
	}
	r.out.Reached = verified == r.c.Fleet.Target
}

// stepFails reports whether a step fails given the world, and records the first cause.
func (r *run) stepFails(st plan.Step, verified string) bool {
	cause := ""
	switch st.Kind {
	case plan.Backup:
		if !r.space {
			cause = "backup-space"
		}
	case plan.Migrate:
		if r.s.migrateFail[st.Version] {
			cause = "migration"
		}
	case plan.Messaging:
		if r.queued {
			r.out.MessageLoss = true
			r.queued = false
		}
	case plan.Core:
		if !accepts(r.cat, st.Version, r.configFor(st.Version)) {
			cause = "config"
		} else if r.s.stall[st.Version][0] {
			cause = "rollout"
		}
	case plan.Gateway:
		if r.s.stall[st.Version][1] {
			cause = "rollout"
		}
	case plan.Verify:
		if !r.health {
			cause = "health"
		}
	}
	if cause != "" && r.out.Cause == "" {
		r.out.Cause = cause
	}
	return cause != ""
}

// advance accounts for a successful step and returns whether the platform is down afterwards.
func (r *run) advance(st plan.Step, down, manual bool) bool {
	d := r.duration(st, manual)
	if down {
		r.out.DowntimeMin += d
	}
	switch st.Kind {
	case plan.Quiesce:
		return true
	case plan.Core:
		return false
	case plan.Messaging:
		if !down {
			r.out.DowntimeMin += r.c.Minutes.BrokerOutage
		}
	}
	return down
}

func (r *run) duration(st plan.Step, manual bool) float64 {
	m := r.c.Minutes
	d := 0.0
	switch st.Kind {
	case plan.Backup:
		d = r.s.usedGiB * m.BackupPerGiB
	case plan.RestoreBackup:
		d = r.s.usedGiB * m.RestorePerGiB
	case plan.Migrate, plan.MigrateDown:
		rel, _ := r.cat.Get(st.Version)
		d = m.MigrateReversible
		if !rel.Reversible && st.Kind == plan.Migrate {
			d = m.MigrateIrreversible
		}
	case plan.Messaging:
		d = m.Broker
	case plan.Core, plan.Gateway:
		d = m.Rollout
	case plan.Verify:
		d = m.Verify
	case plan.Quiesce:
		d = m.Quiesce
	}
	if manual {
		d += m.ManualStepOverhead
	}
	return d
}

func (r *run) restoreFails(rb []plan.Step) bool {
	return r.s.restoreFail && slices.ContainsFunc(rb, func(x plan.Step) bool { return x.Kind == plan.RestoreBackup })
}

func (r *run) broken(down bool) {
	r.out.Broken, r.out.DataLoss = true, true
	r.out.DowntimeMin += r.c.Minutes.ManualRecovery
	r.out.EngineerMin += r.c.Engineer.ManualRecovery
	r.out.FinalVersion = ""
}

// helm runs `helm upgrade --atomic` once per hop: the migration is a pre-upgrade hook, there is no
// preflight, no backup and no quiesce, and a failed release reverts the Kubernetes objects only.
func (r *run) helm(path []string) {
	e := r.c.Engineer
	prev := r.s.from
	for _, v := range path {
		r.out.EngineerMin += e.HelmAttendPerHop
		rel, _ := r.cat.Get(v)
		old, _ := r.cat.Get(prev)
		migrated := false
		hop := []plan.Step{}
		if rel.Schema != old.Schema {
			hop = append(hop, plan.Step{Kind: plan.Migrate, Version: v, Schema: rel.Schema})
		}
		if rel.Messaging != old.Messaging {
			hop = append(hop, plan.Step{Kind: plan.Messaging, Version: v})
		}
		hop = append(hop, plan.Step{Kind: plan.Core, Version: v}, plan.Step{Kind: plan.Gateway, Version: v}, plan.Step{Kind: plan.Verify, Version: v})
		for _, st := range hop {
			r.out.StepsExecuted++
			if r.stepFails(st, prev) {
				r.out.Failed = true
				r.out.EngineerMin += e.InvestigateFailure
				r.out.RollbackPlan = []string{plan.Step{Kind: plan.Gateway, Version: prev}.String(), plan.Step{Kind: plan.Core, Version: prev}.String()}
				// --atomic reverts the workloads; the schema stays where the hook left it
				if (migrated || st.Kind == plan.Migrate) && !rel.Reversible {
					r.broken(false)
					return
				}
				r.out.CleanRollback = true
				r.out.FinalVersion = prev
				return
			}
			r.advance(st, false, false)
			if st.Kind == plan.Migrate {
				migrated = true
				r.online(st)
			}
		}
		prev = v
		r.out.FinalVersion = v
	}
	r.out.Reached = prev == r.c.Fleet.Target
}

// scripted is today's runbook: a person checks, backs up, stops writers, migrates and rolls the
// services by hand, and on failure works out what to do.
func (r *run) scripted(path []string) {
	e, m := r.c.Engineer, r.c.Minutes
	r.out.EngineerMin += e.ScriptedPrepare
	pol := plan.DefaultPolicy()
	if !r.s.skipsCheck {
		// the runbook's checks, done by hand, catch what the preflight catches
		pf := plan.RunPreflight(r.cat, r.s.from, r.c.Fleet.Target, r.s.config, r.facts(), pol)
		for _, name := range pf.Failed() {
			r.out.Blocked = true
			r.out.BlockedChecks = appendOnce(r.out.BlockedChecks, name)
			switch name {
			case "config":
				r.s.config, r.out.EngineerMin = fixTypo(r.s.config), r.out.EngineerMin+e.FixConfig
			case "backup-space":
				r.space, r.out.EngineerMin = true, r.out.EngineerMin+e.GrowVolume
			case "health":
				r.health, r.out.EngineerMin = true, r.out.EngineerMin+e.FixHealth
			case "queues-drained":
				r.queued, r.out.EngineerMin = false, r.out.EngineerMin+m.ManualStepOverhead
			}
		}
	}
	if r.s.missBackup {
		pol.BackupBeforeMigration = false
	}
	steps := plan.Build(r.cat, r.s.from, path, pol)
	verified, down := r.s.from, false
	attended := 0.0
	for i, st := range steps {
		r.out.StepsExecuted++
		attended += r.duration(st, true)
		if st.Kind == plan.Quiesce && r.s.missQuiesce {
			continue // writers stay up through the migration
		}
		if !r.stepFails(st, verified) {
			down = r.advance(st, down, true)
			if st.Kind == plan.Migrate && !down {
				r.online(st)
			}
			if st.Kind == plan.Verify {
				verified = st.Version
				r.out.FinalVersion = verified
			}
			continue
		}
		r.out.Failed = true
		r.out.EngineerMin += attended + m.Diagnose
		target, rb := plan.Rollback(r.cat, r.s.from, steps, i)
		for _, x := range rb {
			r.out.RollbackPlan = append(r.out.RollbackPlan, x.String())
		}
		if slices.ContainsFunc(rb, func(x plan.Step) bool { return x.Kind == plan.ManualRestore }) || r.restoreFails(rb) {
			r.broken(down)
			return
		}
		rbMin := m.Diagnose
		for _, x := range rb {
			rbMin += r.duration(x, true)
		}
		if down || st.Kind == plan.Migrate {
			r.out.DowntimeMin += rbMin
		}
		r.out.EngineerMin += rbMin
		if r.s.missQuiesce && slices.ContainsFunc(rb, func(x plan.Step) bool { return x.Kind == plan.RestoreBackup }) {
			r.out.DataLoss = true // writes after the backup are gone with the restore
		}
		r.out.CleanRollback = !r.out.DataLoss
		r.out.FinalVersion = target
		return
	}
	r.out.EngineerMin += attended
	r.out.Reached = verified == r.c.Fleet.Target
}

// online accounts for an irreversible migration run while writers are up: the platform is
// degraded for part of it, and some sites lose writes.
func (r *run) online(st plan.Step) {
	rel, _ := r.cat.Get(st.Version)
	if rel.Reversible {
		return
	}
	r.out.DowntimeMin += r.c.Minutes.MigrateIrreversible / 2
	if r.s.writeLoss {
		r.out.DataLoss = true
		if r.out.Cause == "" {
			r.out.Cause = "online-migration"
		}
	}
}

// configFor is the config release v is started with. The operator migrates it in code; a person
// (scripted, Helm values) does the same by hand, except that a missed rename leaves the old key.
func (r *run) configFor(v string) map[string]string {
	upto := []string{}
	for _, p := range r.path {
		upto = append(upto, p)
		if p == v {
			break
		}
	}
	cfg, _ := plan.MigrateConfig(r.cat, r.s.config, upto)
	if r.s.missRename && (r.d == Scripted || r.d == HelmAtomic) {
		for _, p := range upto {
			rel, _ := r.cat.Get(p)
			for old, nu := range rel.Config.Renames {
				if val, ok := cfg[nu]; ok {
					delete(cfg, nu)
					cfg[old] = val
				}
			}
		}
	}
	return cfg
}

// accepts reports whether release v starts with this config as it is (no migration applied).
func accepts(cat *release.Catalog, v string, cfg map[string]string) bool {
	rel, _ := cat.Get(v)
	rules := rel.Config
	for k := range cfg {
		if !slices.Contains(rules.Known, k) {
			return false
		}
	}
	for _, k := range rules.Required {
		if _, ok := cfg[k]; !ok {
			return false
		}
	}
	for k, allowed := range rules.Values {
		if val, ok := cfg[k]; ok && !slices.Contains(allowed, val) {
			return false
		}
	}
	return true
}

func fixTypo(cfg map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range cfg {
		out[k] = v
	}
	if out["audit.level"] == "ful" {
		out["audit.level"] = "full"
	}
	return out
}

func appendOnce(xs []string, x string) []string {
	if slices.Contains(xs, x) {
		return xs
	}
	return append(xs, x)
}
