package plan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
)

var cat = release.Default()

func base() map[string]string {
	return map[string]string{"retention.days": "180", "export.format": "xml", "mq.prefetch": "50", "audit.level": "full"}
}

func healthy() Facts {
	return Facts{ComponentsReady: true, DatabaseUsedBytes: 20 << 30, DatabaseCapacityBytes: 100 << 30}
}

func TestConfigMigratesThroughRenamesDefaultsAndRemovals(t *testing.T) {
	out, fs := MigrateConfig(cat, base(), []string{"4.3.0", "5.0.0", "5.1.0"})
	for _, f := range fs {
		if f.Severity == Error {
			t.Fatalf("unexpected error finding: %+v", f)
		}
	}
	want := map[string]string{"retention.days": "180", "handover.format": "xml", "messaging.prefetch": "50", "audit.level": "full", "tls.minVersion": "1.2"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("migrated config = %v, want %v", out, want)
	}
	if base()["export.format"] != "xml" {
		t.Fatal("input must not be modified")
	}
}

func TestConfigFindsTyposMissingKeysAndBadValues(t *testing.T) {
	cfg := base()
	cfg["retention.day"] = "30" // typo
	cfg["export.format"] = "json"
	_, fs := MigrateConfig(cat, cfg, []string{"4.3.0", "5.0.0"})
	msgs := ""
	for _, f := range fs {
		msgs += f.Message + "\n"
	}
	for _, want := range []string{"did you mean retention.days", "handover.format=json is not allowed"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("findings missing %q:\n%s", want, msgs)
		}
	}
	_, fs = MigrateConfig(cat, map[string]string{"export.format": "xml"}, []string{"4.3.0"})
	if len(fs) == 0 || fs[len(fs)-1].Message != "retention.days is required by 4.3.0" {
		t.Errorf("missing required key not reported: %+v", fs)
	}
}

func TestPreflightPassesAHealthyUpgrade(t *testing.T) {
	pf := RunPreflight(cat, "4.3.0", "5.0.0", base(), healthy(), DefaultPolicy())
	if !pf.OK() {
		t.Fatalf("expected OK, failed: %v %+v", pf.Failed(), pf.Checks)
	}
	names := []string{}
	for _, c := range pf.Checks {
		names = append(names, c.Name)
	}
	if want := []string{"path", "config", "health", "backup-space", "queues-drained"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("checks = %v, want %v", names, want)
	}
}

func TestPreflightBlocksBeforeTouchingAnything(t *testing.T) {
	full := healthy()
	full.DatabaseCapacityBytes = 30 << 30 // 10 GiB free for a 20 GiB database
	full.QueueDepth = 1200
	full.ComponentsReady = false
	pf := RunPreflight(cat, "4.3.0", "5.0.0", base(), full, DefaultPolicy())
	if want := []string{"health", "backup-space", "queues-drained"}; !reflect.DeepEqual(pf.Failed(), want) {
		t.Fatalf("failed = %v, want %v", pf.Failed(), want)
	}
	// a reversible hop needs no backup space
	pf = RunPreflight(cat, "5.0.0", "5.1.0", map[string]string{"retention.days": "1", "handover.format": "xml"}, Facts{ComponentsReady: true, DatabaseUsedBytes: 90 << 30, DatabaseCapacityBytes: 100 << 30}, DefaultPolicy())
	if !pf.OK() {
		t.Fatalf("reversible hop should pass: %v", pf.Failed())
	}
	if pf := RunPreflight(cat, "5.1.0", "4.2.0", base(), healthy(), DefaultPolicy()); pf.OK() || pf.Failed()[0] != "path" {
		t.Fatal("downgrade must fail the path check")
	}
}

func TestBuildOrdersEachHop(t *testing.T) {
	got := Build(cat, "4.2.0", []string{"4.3.0", "5.0.0"}, DefaultPolicy())
	want := []Step{
		{Migrate, "4.3.0", 13}, {Kind: Core, Version: "4.3.0"}, {Kind: Gateway, Version: "4.3.0"}, {Kind: Verify, Version: "4.3.0"},
		{Backup, "5.0.0", 13}, {Kind: Quiesce, Version: "5.0.0"}, {Migrate, "5.0.0", 20}, {Kind: Messaging, Version: "5.0.0"},
		{Kind: Core, Version: "5.0.0"}, {Kind: Gateway, Version: "5.0.0"}, {Kind: Verify, Version: "5.0.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan =\n%v\nwant\n%v", got, want)
	}
	noBackup := Build(cat, "4.3.0", []string{"5.0.0"}, Policy{})
	if noBackup[0].Kind != Quiesce {
		t.Fatalf("without backup policy the plan starts with %v", noBackup[0])
	}
}

func TestRollbackRestoresTheLastVerifiedRelease(t *testing.T) {
	steps := Build(cat, "4.2.0", []string{"4.3.0", "5.0.0"}, DefaultPolicy())
	at := indexOf(steps, Core, "5.0.0") // the new core crashes after the irreversible migration
	target, rb := Rollback(cat, "4.2.0", steps, at)
	if target != "4.3.0" {
		t.Fatalf("target = %s, want the verified 4.3.0", target)
	}
	want := []Step{{Kind: Quiesce, Version: "4.3.0"}, {RestoreBackup, "4.3.0", 13}, {Kind: Core, Version: "4.3.0"}, {Kind: Verify, Version: "4.3.0"}}
	if !reflect.DeepEqual(rb, want) {
		t.Fatalf("rollback = %v, want %v", rb, want)
	}
}

func TestRollbackReversesAReversibleMigration(t *testing.T) {
	steps := Build(cat, "5.0.0", []string{"5.1.0"}, DefaultPolicy())
	_, rb := Rollback(cat, "5.0.0", steps, indexOf(steps, Gateway, "5.1.0"))
	kinds := []Kind{}
	for _, s := range rb {
		kinds = append(kinds, s.Kind)
	}
	if want := []Kind{Gateway, Quiesce, MigrateDown, Core, Verify}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("rollback kinds = %v, want %v", kinds, want)
	}
}

func TestRollbackWithoutBackupNeedsAPerson(t *testing.T) {
	steps := Build(cat, "4.3.0", []string{"5.0.0"}, Policy{})
	_, rb := Rollback(cat, "4.3.0", steps, indexOf(steps, Migrate, "5.0.0"))
	if rb[len(rb)-1].Kind != ManualRestore {
		t.Fatalf("expected ManualRestore, got %v", rb)
	}
	// a failed backup is not a backup either, but nothing has changed yet: just verify
	steps = Build(cat, "4.3.0", []string{"5.0.0"}, DefaultPolicy())
	_, rb = Rollback(cat, "4.3.0", steps, 0)
	if len(rb) != 1 || rb[0].Kind != Verify {
		t.Fatalf("abort before any change should only verify, got %v", rb)
	}
}

func indexOf(steps []Step, k Kind, v string) int {
	for i, s := range steps {
		if s.Kind == k && s.Version == v {
			return i
		}
	}
	return -1
}
