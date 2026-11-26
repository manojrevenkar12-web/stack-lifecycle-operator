package lab

import (
	"path/filepath"
	"testing"

	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
)

func load(t *testing.T) (*Config, []byte) {
	t.Helper()
	c, raw, err := LoadConfig(filepath.Join("..", "..", "config", "lab.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return c, raw
}

func TestSameSeedSameResult(t *testing.T) {
	c, raw := load(t)
	c.Fleet.Sites = 50
	a := Run(c, raw, release.Default(), "dev", []uint64{7})
	b := Run(c, raw, release.Default(), "dev", []uint64{7})
	for i := range a.Designs {
		for k, v := range a.Designs[i].Metrics {
			if b.Designs[i].Metrics[k] != v {
				t.Fatalf("%s %s differs between identical runs", a.Designs[i].Design, k)
			}
		}
	}
}

func TestEveryDesignSeesTheSameSites(t *testing.T) {
	c, _ := load(t)
	cat := release.Default()
	x, y := Fleet(c, cat, 3), Fleet(c, cat, 3)
	for i := range x {
		if x[i].from != y[i].from || x[i].migrateFail["5.0.0"] != y[i].migrateFail["5.0.0"] || x[i].usedGiB != y[i].usedGiB {
			t.Fatal("site draws must not depend on anything but the seed and the site index")
		}
	}
}

// a clean site upgrades under every design
func TestCleanSiteUpgradesEverywhere(t *testing.T) {
	c, _ := load(t)
	cat := release.Default()
	s := clean(cat, "4.3.0")
	for _, d := range Designs {
		o := Simulate(c, cat, s, d)
		if !o.FirstTry || o.FinalVersion != "5.1.0" || o.DataLoss || o.Broken {
			t.Fatalf("%s: %+v", d, o)
		}
	}
}

func clean(cat *release.Catalog, from string) site {
	s := site{from: from, usedGiB: 50, migrateFail: map[string]bool{}, stall: map[string][2]bool{}}
	for _, r := range cat.Releases {
		s.migrateFail[r.Version], s.stall[r.Version] = false, [2]bool{}
	}
	s.config = map[string]string{"retention.days": "180", "export.format": "xml", "mq.prefetch": "50", "audit.level": "full"}
	return s
}

func TestFailedIrreversibleMigration(t *testing.T) {
	c, _ := load(t)
	cat := release.Default()
	s := clean(cat, "4.3.0")
	s.migrateFail["5.0.0"] = true
	op := Simulate(c, cat, s, Operator)
	if !op.CleanRollback || op.FinalVersion != "4.3.0" || op.DataLoss {
		t.Fatalf("operator must restore the backup and stay on 4.3.0: %+v", op)
	}
	if want := []string{"Quiesce(4.3.0)", "RestoreBackup(4.3.0)", "Core(4.3.0)", "Verify(4.3.0)"}; !equal(op.RollbackPlan, want) {
		t.Fatalf("rollback plan %v", op.RollbackPlan)
	}
	if o := Simulate(c, cat, s, OperatorNoBackup); !o.Broken || !o.DataLoss {
		t.Fatalf("without a backup the site is broken: %+v", o)
	}
	if o := Simulate(c, cat, s, HelmAtomic); !o.Broken {
		t.Fatalf("--atomic does not restore the schema: %+v", o)
	}
}

func TestPreflightCatchesQueuesAndTypos(t *testing.T) {
	c, _ := load(t)
	cat := release.Default()
	s := clean(cat, "4.3.0")
	s.queued = true
	s.config["audit.level"] = "ful"
	s.typo = true
	op := Simulate(c, cat, s, Operator)
	if !op.Blocked || op.MessageLoss || !op.Reached || len(op.BlockedChecks) != 2 {
		t.Fatalf("preflight must block, then the fixed site upgrades: %+v", op)
	}
	np := Simulate(c, cat, s, OperatorNoPreflight)
	if !np.MessageLoss || !np.Failed || np.Cause != "config" {
		t.Fatalf("without preflight the queue is lost and 5.0.0 refuses the config: %+v", np)
	}
}

func TestRolloutStallOnReversibleHopNeedsNoDowntime(t *testing.T) {
	c, _ := load(t)
	cat := release.Default()
	s := clean(cat, "5.0.0")
	s.config = map[string]string{"retention.days": "180", "handover.format": "xml", "audit.level": "full"}
	s.stall["5.1.0"] = [2]bool{false, true}
	o := Simulate(c, cat, s, Operator)
	if !o.CleanRollback || o.FinalVersion != "5.0.0" || o.DowntimeMin != 0 {
		t.Fatalf("%+v", o)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
