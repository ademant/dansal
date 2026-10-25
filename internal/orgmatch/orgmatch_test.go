package orgmatch

import "testing"

func TestNormalizeName(t *testing.T) {
	tests := map[string]string{
		"Erlanger Tanzhaus":      "erlangertanzhaus",
		"Erlangener Tanzhaus":    "erlangenertanzhaus",
		"Tanzhaus Erlangen e.V.": "tanzhauserlangen",
		"Balfolk-Verein München": "balfolkmunchen",
		"Café Ö & Co. gGmbH":     "cafeoco",
		"  Folk   Club  ":        "folkclub",
	}
	for in, want := range tests {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNameSimilar(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"Erlanger Tanzhaus", "Erlangener Tanzhaus", true}, // two-letter typo
		{"Tanzhaus Nord", "Tanzhaus Süd", false},           // different words, short
		{"Balfolk Berlin", "Balfolk Berlin e.V.", true},    // legal form only
		{"Folk", "Folx", false},                            // below minimum length
		{"Tanzhaus Erlangen", "Tanzschule Bamberg", false},
	}
	for _, tc := range tests {
		if got := NameSimilar(tc.a, tc.b); got != tc.want {
			t.Errorf("NameSimilar(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSiteKey(t *testing.T) {
	tests := []struct {
		raw, host, path string
		ok              bool
	}{
		{"https://www.Tanzhaus.example/", "tanzhaus.example", "", true},
		{"tanzhaus.example/erlangen/", "tanzhaus.example", "/erlangen", true},
		{"http://tanzhaus.example/Erlangen", "tanzhaus.example", "/erlangen", true},
		{"", "", "", false},
		{"https://www.facebook.com/tanzhaus", "", "", false}, // shared platform
		{"https://tanz.gmail.com", "", "", false},            // platform subdomain
	}
	for _, tc := range tests {
		host, path, ok := SiteKey(tc.raw)
		if host != tc.host || path != tc.path || ok != tc.ok {
			t.Errorf("SiteKey(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.raw, host, path, ok, tc.host, tc.path, tc.ok)
		}
	}
}

func TestFind(t *testing.T) {
	existing := []Org{
		{ID: 1, Name: "Erlanger Tanzhaus", Website: "https://tanzhaus.example"},
		{ID: 2, Name: "WP Berlin", Website: "https://community.example/berlin"},
		{ID: 3, Name: "WP Bamberg", Website: "https://community.example/bamberg"},
		{ID: 4, Name: "Social Folk", Website: "https://www.facebook.com/socialfolk"},
		{ID: 5, Name: "Tanzhaus Nord", ActorName: "tanzhaus_nord"},
	}
	tests := []struct {
		name string
		req  Request
		want []int // org IDs offered, strong first
	}{
		{
			"same host and path is a strong offer on its own",
			Request{Name: "Anderer Name", Website: "www.tanzhaus.example/"},
			[]int{1},
		},
		{
			"same host, different path, similar name is offered",
			Request{Name: "WP Berlin", Website: "https://community.example/berlin-neu"},
			[]int{2},
		},
		{
			"same host, different path, no name signal is not offered",
			Request{Name: "Balfolk Hamburg", Website: "https://community.example/hamburg"},
			[]int{},
		},
		{
			"shared platform host never counts",
			Request{Name: "Social Folk", Website: "https://facebook.com/socialfolk-neu"},
			[]int{},
		},
		{
			"name alone is not offered",
			Request{Name: "Erlangener Tanzhaus"},
			[]int{},
		},
		{
			"activitypub name alone is not offered",
			Request{Name: "Anderer", ActorName: "tanzhaus_nord"},
			[]int{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Find(tc.req, existing)
			if len(got) != len(tc.want) {
				t.Fatalf("offers = %+v, want IDs %v", got, tc.want)
			}
			for i, o := range got {
				if o.Org.ID != tc.want[i] {
					t.Fatalf("offer %d = org %d, want %d", i, o.Org.ID, tc.want[i])
				}
			}
		})
	}
}

// TestFindSameHostPathWithTypo covers the registration case from #1648:
// "Erlanger" and "Erlangener" on the same website are offered as a strong match.
func TestFindSameHostPathWithTypo(t *testing.T) {
	existing := []Org{{ID: 7, Name: "Erlanger Tanzhaus", Website: "https://tanzhaus-erlangen.example"}}
	got := Find(Request{Name: "Erlangener Tanzhaus", Website: "https://tanzhaus-erlangen.example"}, existing)
	if len(got) != 1 || got[0].Org.ID != 7 || !got[0].Strong {
		t.Fatalf("offers = %+v, want one strong offer for org 7", got)
	}
}

// TestFindHostOnlyWithNameIsOffered covers the weak combination: the host
// matches but the path differs (another WordPress instance), and the names
// are similar.
func TestFindHostOnlyWithNameIsOffered(t *testing.T) {
	existing := []Org{{ID: 9, Name: "Balfolk Erlangen", Website: "https://community.example/erlangen"}}
	got := Find(Request{Name: "Balfolk Erlangen e.V.", Website: "https://community.example/other"}, existing)
	if len(got) != 1 || got[0].Org.ID != 9 || got[0].Strong {
		t.Fatalf("offers = %+v, want one weak offer for org 9", got)
	}
}
