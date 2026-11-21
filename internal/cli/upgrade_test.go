package cli

import (
	"reflect"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/upgrade"
)

func TestSplitUpgradeEntries(t *testing.T) {
	entries := []upgrade.Entry{
		{Name: "app", Kind: upgrade.KindUpgrade},
		{Name: "newtool", Kind: upgrade.KindInstall},
		{Name: "buildtool", Kind: upgrade.KindInstall},
		{Name: "rebuild", Kind: upgrade.KindRebuild},
		{Name: "old", Kind: upgrade.KindDowngrade},
		{Name: "lib", Kind: upgrade.KindUpgrade},
	}
	newDeps, changes := splitUpgradeEntries(entries)
	if want := []string{"newtool", "buildtool"}; !reflect.DeepEqual(newDeps, want) {
		t.Fatalf("newDeps = %v, want %v", newDeps, want)
	}
	if want := []string{"app", "rebuild", "old", "lib"}; !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %v, want %v", changes, want)
	}
}

func TestSplitUpgradeEntriesOnlyNew(t *testing.T) {
	newDeps, changes := splitUpgradeEntries([]upgrade.Entry{
		{Name: "a", Kind: upgrade.KindInstall},
	})
	if len(newDeps) != 1 || newDeps[0] != "a" || len(changes) != 0 {
		t.Fatalf("newDeps = %v, changes = %v", newDeps, changes)
	}
}
