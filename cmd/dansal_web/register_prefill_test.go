package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReadRegisterPrefillRoundTrip locks in the query-param contract between
// fetchSuggestAccountValues (the feed-suggestion submit/done handlers) and
// readRegisterPrefill (the /register page) -- both sides must agree on field
// names for the "create an account" link (#1336) to actually prefill anything.
func TestReadRegisterPrefillRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		fields fetchSuggestFormFields
	}{
		{
			name: "existing org",
			fields: fetchSuggestFormFields{
				Email:     "join@example.com",
				OrgChoice: "existing",
				OrgID:     7,
			},
		},
		{
			name: "new org",
			fields: fetchSuggestFormFields{
				Email:           "new@example.com",
				OrgChoice:       "new",
				OrgName:         "New Org",
				OrgActorName:    "neworg",
				OrgDescription:  "desc",
				OrgWebsite:      "https://neworg.example",
				OrgContactEmail: "contact@example.com",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := fetchSuggestAccountValues(tc.fields)
			r := httptest.NewRequest(http.MethodGet, "/register?"+v.Encode(), nil)
			regType, orgID, orgName, orgActorName, orgDesc, orgWebsite, orgContactEmail, email := readRegisterPrefill(r)

			if tc.fields.OrgChoice == "new" {
				if regType != "new_org" {
					t.Errorf("regType=%q, want new_org", regType)
				}
				if orgName != tc.fields.OrgName || orgActorName != tc.fields.OrgActorName ||
					orgDesc != tc.fields.OrgDescription || orgWebsite != tc.fields.OrgWebsite ||
					orgContactEmail != tc.fields.OrgContactEmail {
					t.Errorf("new-org fields did not round-trip: got %+v", []string{orgName, orgActorName, orgDesc, orgWebsite, orgContactEmail})
				}
			} else {
				if regType != "join_org" {
					t.Errorf("regType=%q, want join_org", regType)
				}
				if orgID != tc.fields.OrgID {
					t.Errorf("orgID=%d, want %d", orgID, tc.fields.OrgID)
				}
			}
			if email != tc.fields.Email {
				t.Errorf("email=%q, want %q", email, tc.fields.Email)
			}
		})
	}
}
