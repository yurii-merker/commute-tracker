package station

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/sahilm/fuzzy"
)

//go:embed data/stations.json
var stationsFS embed.FS

type Result struct {
	CRS  string
	Name string
}

type entry struct {
	CRS  string `json:"crs"`
	Name string `json:"name"`
}

type stationList []entry

func (s stationList) String(i int) string { return s[i].Name }
func (s stationList) Len() int            { return len(s) }

var (
	once       sync.Once
	errLoad    error
	stations   map[string]string
	allEntries stationList
)

func load() {
	data, err := stationsFS.ReadFile("data/stations.json")
	if err != nil {
		errLoad = fmt.Errorf("reading embedded stations data: %w", err)
		return
	}

	if err := json.Unmarshal(data, &allEntries); err != nil {
		errLoad = fmt.Errorf("parsing stations data: %w", err)
		return
	}

	stations = make(map[string]string, len(allEntries))
	for _, e := range allEntries {
		stations[strings.ToUpper(e.CRS)] = e.Name
	}
}

func Init() error {
	once.Do(load)
	return errLoad
}

func Lookup(crs string) (string, bool) {
	once.Do(load)
	name, ok := stations[strings.ToUpper(crs)]
	return name, ok
}

func IsValid(crs string) bool {
	_, ok := Lookup(crs)
	return ok
}

func Search(query string) []Result {
	once.Do(load)

	matches := fuzzy.FindFrom(query, allEntries)

	const maxResults = 5
	seen := make(map[string]bool)
	var results []Result

	for _, m := range matches {
		e := allEntries[m.Index]
		if seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		results = append(results, Result(e))
		if len(results) >= maxResults {
			break
		}
	}

	return results
}
