// Package release holds the release catalog of the managed platform: versions, the database
// schema each needs, which versions may upgrade to it, and how its application config changes.
package release

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed catalog.json
var catalogJSON []byte

// ConfigRules describe the application config a release accepts and how it changes from the
// release before it.
type ConfigRules struct {
	Known    []string            `json:"known"`
	Required []string            `json:"required"`
	Defaults map[string]string   `json:"defaults,omitempty"`
	Renames  map[string]string   `json:"renames,omitempty"`
	Removed  []string            `json:"removed,omitempty"`
	Values   map[string][]string `json:"values,omitempty"`
}

// Release is one shippable version of the platform.
type Release struct {
	Version     string      `json:"version"`
	Schema      int         `json:"schema"`
	Messaging   string      `json:"messaging"`
	UpgradeFrom []string    `json:"upgradeFrom"`
	Reversible  bool        `json:"reversible"`
	Config      ConfigRules `json:"config"`
}

// Catalog is every release, oldest first.
type Catalog struct {
	Releases []Release `json:"releases"`
	byVer    map[string]Release
}

// Default returns the embedded catalog.
func Default() *Catalog {
	c, err := Parse(catalogJSON)
	if err != nil {
		panic(err)
	}
	return c
}

// Parse reads a catalog and checks that every upgrade edge points at a known release.
func Parse(data []byte) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	c.byVer = map[string]Release{}
	for _, r := range c.Releases {
		if _, dup := c.byVer[r.Version]; dup {
			return nil, fmt.Errorf("catalog: duplicate release %s", r.Version)
		}
		c.byVer[r.Version] = r
	}
	for _, r := range c.Releases {
		for _, f := range r.UpgradeFrom {
			if _, ok := c.byVer[f]; !ok {
				return nil, fmt.Errorf("catalog: %s upgrades from unknown %s", r.Version, f)
			}
			if Compare(f, r.Version) >= 0 {
				return nil, fmt.Errorf("catalog: %s upgrades from newer or equal %s", r.Version, f)
			}
		}
	}
	sort.Slice(c.Releases, func(i, j int) bool { return Compare(c.Releases[i].Version, c.Releases[j].Version) < 0 })
	return &c, nil
}

// Get returns a release by version.
func (c *Catalog) Get(v string) (Release, bool) {
	r, ok := c.byVer[v]
	return r, ok
}

// Latest is the newest release.
func (c *Catalog) Latest() Release { return c.Releases[len(c.Releases)-1] }

// Path is the shortest chain of supported upgrades from one version to another, excluding the
// start. Downgrades are not supported; a rollback restores the previous state instead.
func (c *Catalog) Path(from, to string) ([]string, error) {
	if _, ok := c.byVer[from]; !ok {
		return nil, fmt.Errorf("unknown installed version %q", from)
	}
	if _, ok := c.byVer[to]; !ok {
		return nil, fmt.Errorf("unknown target version %q", to)
	}
	if from == to {
		return nil, nil
	}
	if Compare(to, from) < 0 {
		return nil, fmt.Errorf("downgrade from %s to %s is not supported", from, to)
	}
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, r := range c.Releases {
			if _, seen := prev[r.Version]; seen {
				continue
			}
			for _, f := range r.UpgradeFrom {
				if f == cur {
					prev[r.Version] = cur
					queue = append(queue, r.Version)
				}
			}
		}
	}
	if _, ok := prev[to]; !ok {
		return nil, fmt.Errorf("no supported upgrade path from %s to %s", from, to)
	}
	var path []string
	for v := to; v != from; v = prev[v] {
		path = append([]string{v}, path...)
	}
	return path, nil
}

// Compare orders dotted numeric versions.
func Compare(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
