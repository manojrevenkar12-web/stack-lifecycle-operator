package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"math/rand/v2"
	"sort"

	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
)

// Metric is one number reported per design.
type Metric struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Unit   string `json:"unit"`
	Better string `json:"better"` // "higher", "lower", or "context" (read next to another measure)
}

// Metrics in report order. Counts are per 100 site upgrades.
var Metrics = []Metric{
	{"first_try", "Upgraded cleanly on the first try", "per 100", "higher"},
	{"reached", "On the target release at the end", "per 100", "higher"},
	{"preflight_blocks", "Stopped before any change", "per 100", "lower"},
	{"failed", "Upgrades with a failed step", "per 100", "lower"},
	{"clean_rollbacks", "Failed, then back on the last good release", "per 100", "context"},
	{"broken", "Sites left broken", "per 100", "lower"},
	{"data_loss", "Data-loss incidents", "per 100", "lower"},
	{"message_loss", "Message-loss incidents", "per 100", "lower"},
	{"downtime", "Downtime per upgrade", "minutes", "lower"},
	{"engineer", "Engineer time", "hours per 100", "lower"},
}

// Stat is a mean over seeds with a 95% interval (normal approximation).
type Stat struct {
	Mean float64 `json:"mean"`
	CI95 float64 `json:"ci95"`
}

// DesignResult is one design's numbers over a split.
type DesignResult struct {
	Design      Design          `json:"design"`
	Label       string          `json:"label"`
	Metrics     map[string]Stat `json:"metrics"`
	DowntimeP95 float64         `json:"downtime_p95_minutes"`
	Causes      map[string]int  `json:"failure_causes"` // first cause of each failed or lossy upgrade, all seeds
	Blocks      map[string]int  `json:"blocked_checks"`
}

// Result of a split.
type Result struct {
	Split        string         `json:"split"`
	Seeds        []uint64       `json:"seeds"`
	SitesPerSeed int            `json:"sites_per_seed"`
	Target       string         `json:"target"`
	StartMix     map[string]int `json:"start_mix"` // sites per start release, first seed
	ConfigSHA256 string         `json:"config_sha256"`
	Designs      []DesignResult `json:"designs"`
	Example      []Example      `json:"example"`
}

// Example shows one site's outcome under every design, for the README.
type Example struct {
	Design   Design   `json:"design"`
	From     string   `json:"from"`
	Final    string   `json:"final"`
	Cause    string   `json:"cause"`
	Rollback []string `json:"rollback"`
	Downtime float64  `json:"downtime_minutes"`
}

// Hash of the settings, stored with results so a changed config is noticed.
func Hash(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// Fleet draws the sites of one seed.
func Fleet(c *Config, cat *release.Catalog, seed uint64) []site {
	out := make([]site, c.Fleet.Sites)
	for i := range out {
		out[i] = drawSite(c, cat, rand.New(rand.NewPCG(seed, uint64(i))))
	}
	return out
}

// Run simulates every design on every seed of a split.
func Run(c *Config, raw []byte, cat *release.Catalog, split string, seeds []uint64) Result {
	res := Result{Split: split, Seeds: seeds, SitesPerSeed: c.Fleet.Sites, Target: c.Fleet.Target, ConfigSHA256: Hash(raw), StartMix: map[string]int{}}
	perSeed := map[Design]map[string][]float64{}
	downs := map[Design][]float64{}
	causes := map[Design]map[string]int{}
	blocks := map[Design]map[string]int{}
	for _, d := range Designs {
		perSeed[d] = map[string][]float64{}
		causes[d], blocks[d] = map[string]int{}, map[string]int{}
	}
	for si, seed := range seeds {
		sites := Fleet(c, cat, seed)
		ex := exampleSite(sites)
		if si == 0 {
			for _, s := range sites {
				res.StartMix[s.from]++
			}
		}
		for _, d := range Designs {
			var first, reached, blocked, failed, clean, broken, data, msg, down, eng float64
			for i, s := range sites {
				o := Simulate(c, cat, s, d)
				first += b2f(o.FirstTry)
				reached += b2f(o.Reached)
				blocked += b2f(o.Blocked)
				failed += b2f(o.Failed)
				clean += b2f(o.CleanRollback)
				broken += b2f(o.Broken)
				data += b2f(o.DataLoss)
				msg += b2f(o.MessageLoss)
				down += o.DowntimeMin
				eng += o.EngineerMin
				downs[d] = append(downs[d], o.DowntimeMin)
				if o.Cause != "" {
					causes[d][o.Cause]++
				} else if o.MessageLoss {
					causes[d]["queues-not-drained"]++
				}
				for _, b := range o.BlockedChecks {
					blocks[d][b]++
				}
				if si == 0 && i == ex {
					res.Example = append(res.Example, Example{d, s.from, o.FinalVersion, o.Cause, o.RollbackPlan, round(o.DowntimeMin, 1)})
				}
			}
			n := float64(len(sites))
			add := func(k string, v float64) { perSeed[d][k] = append(perSeed[d][k], v) }
			add("first_try", 100*first/n)
			add("reached", 100*reached/n)
			add("preflight_blocks", 100*blocked/n)
			add("failed", 100*failed/n)
			add("clean_rollbacks", 100*clean/n)
			add("broken", 100*broken/n)
			add("data_loss", 100*data/n)
			add("message_loss", 100*msg/n)
			add("downtime", down/n)
			add("engineer", 100*eng/n/60)
		}
	}
	for _, d := range Designs {
		dr := DesignResult{Design: d, Label: d.Label(), Metrics: map[string]Stat{}, Causes: causes[d], Blocks: blocks[d]}
		for _, m := range Metrics {
			dr.Metrics[m.Key] = stat(perSeed[d][m.Key])
		}
		dr.DowntimeP95 = round(percentile(downs[d], 0.95), 1)
		res.Designs = append(res.Designs, dr)
	}
	return res
}

// exampleSite picks the first site on 4.3.0 whose irreversible migration fails: the case that
// separates the designs most clearly.
func exampleSite(sites []site) int {
	for i, s := range sites {
		if s.from == "4.3.0" && s.migrateFail["5.0.0"] && !s.lowSpace && !s.unhealthy && !s.typo && !s.queued && !s.restoreFail && !s.skipsCheck && !s.missBackup && !s.missQuiesce && !s.missRename {
			return i
		}
	}
	return 0
}

func stat(xs []float64) Stat {
	if len(xs) == 0 {
		return Stat{}
	}
	mean := 0.0
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	if len(xs) < 2 {
		return Stat{round(mean, 2), 0}
	}
	v := 0.0
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	sd := math.Sqrt(v / float64(len(xs)-1))
	return Stat{round(mean, 2), round(1.96*sd/math.Sqrt(float64(len(xs))), 2)}
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[int(math.Ceil(p*float64(len(s))))-1]
}

func round(x float64, d int) float64 {
	f := math.Pow(10, float64(d))
	return math.Round(x*f) / f
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
