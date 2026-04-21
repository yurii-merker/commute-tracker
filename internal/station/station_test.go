package station

import "testing"

func TestLookup(t *testing.T) {
	tests := []struct {
		crs      string
		wantName string
		wantOK   bool
	}{
		{"KGX", "London Kings Cross", true},
		{"PAD", "London Paddington", true},
		{"BRI", "Bristol Temple Meads", true},
		{"SMY", "St Mary Cray", true},
		{"CTK", "City Thameslink", true},
		{"kgx", "London Kings Cross", true},
		{"ZZZ", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.crs, func(t *testing.T) {
			name, ok := Lookup(tt.crs)
			if ok != tt.wantOK {
				t.Errorf("Lookup(%q) ok = %v, want %v", tt.crs, ok, tt.wantOK)
			}
			if name != tt.wantName {
				t.Errorf("Lookup(%q) name = %q, want %q", tt.crs, name, tt.wantName)
			}
		})
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		crs  string
		want bool
	}{
		{"KGX", true},
		{"pad", true},
		{"ZZZ", false},
		{"", false},
		{"TOOLONG", false},
	}

	for _, tt := range tests {
		t.Run(tt.crs, func(t *testing.T) {
			if got := IsValid(tt.crs); got != tt.want {
				t.Errorf("IsValid(%q) = %v, want %v", tt.crs, got, tt.want)
			}
		})
	}
}

func TestSearchExactMatch(t *testing.T) {
	results := Search("City Thameslink")
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	found := false
	for _, r := range results {
		if r.Name == "City Thameslink" {
			found = true
		}
	}
	if !found {
		t.Error("expected City Thameslink in results")
	}
}

func TestSearchPrefix(t *testing.T) {
	results := Search("Kings Cross")
	found := false
	for _, r := range results {
		if r.CRS == "KGX" {
			found = true
		}
	}
	if !found {
		t.Error("expected KGX in results for 'Kings Cross'")
	}
}

func TestSearchPartial(t *testing.T) {
	results := Search("Brighton")
	if len(results) == 0 {
		t.Fatal("expected results for 'Brighton'")
	}
	found := false
	for _, r := range results {
		if r.CRS == "BTN" {
			found = true
		}
	}
	if !found {
		t.Error("expected BTN in results for 'Brighton'")
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	results := Search("city thameslink")
	if len(results) == 0 {
		t.Fatal("expected results for lowercase search")
	}
	if results[0].Name != "City Thameslink" {
		t.Errorf("expected City Thameslink, got %s", results[0].Name)
	}
}

func TestSearchNoResults(t *testing.T) {
	results := Search("xyznonexistent")
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSearchMaxResults(t *testing.T) {
	results := Search("London")
	if len(results) > 5 {
		t.Errorf("expected max 5 results, got %d", len(results))
	}
}

func TestSearchSingleChar(t *testing.T) {
	results := Search("a")
	if len(results) > 5 {
		t.Errorf("expected max 5 results, got %d", len(results))
	}
}
