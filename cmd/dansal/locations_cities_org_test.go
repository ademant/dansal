package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// #1588: GET /api/v1/locations/cities reports, per town, how many organisations
// have a geo-tagged location there, and the single organisation when there is
// exactly one. The web city page uses both for its banner and description.
func TestGetCitiesOrganisationCounts(t *testing.T) {
	setupDedupTestDB(t)

	db.Exec("INSERT INTO organizations (id, name) VALUES (1, 'Org 1'), (2, 'Org 2')")
	db.Exec(`INSERT INTO locations (id, location, town, latitude, longitude) VALUES
		(1, 'Saal A', 'Bonn', 50.7, 7.1),
		(2, 'Saal B', 'Bonn', 50.8, 7.2),
		(3, 'Halle', 'Köln', 50.9, 6.9),
		(4, 'Hof', 'Köln', 50.95, 6.95)`)
	db.Exec(`INSERT INTO location_organizations (location_id, organization_id) VALUES
		(1, 1), (2, 1), (3, 1), (3, 2), (4, 2)`)

	req := httptest.NewRequest("GET", "/api/v1/locations/cities?include_inactive=true", nil)
	w := httptest.NewRecorder()
	getCities(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var cities []CityInfo
	if err := json.Unmarshal(w.Body.Bytes(), &cities); err != nil {
		t.Fatalf("body is not a city list: %v", err)
	}
	byTown := map[string]CityInfo{}
	for _, c := range cities {
		byTown[c.Town] = c
	}

	bonn, ok := byTown["Bonn"]
	if !ok {
		t.Fatalf("Bonn missing from %s", w.Body.String())
	}
	if bonn.OrgCount != 1 || bonn.SingleOrgID == nil || *bonn.SingleOrgID != 1 {
		t.Errorf("Bonn: org_count=%d single_org_id=%v, want 1 and 1", bonn.OrgCount, bonn.SingleOrgID)
	}
	if bonn.LocationCount != 2 {
		t.Errorf("Bonn location_count = %d, want 2 (org counts must not change it)", bonn.LocationCount)
	}

	koeln, ok := byTown["Köln"]
	if !ok {
		t.Fatalf("Köln missing from %s", w.Body.String())
	}
	if koeln.OrgCount != 2 || koeln.SingleOrgID != nil {
		t.Errorf("Köln: org_count=%d single_org_id=%v, want 2 and none", koeln.OrgCount, koeln.SingleOrgID)
	}
}
