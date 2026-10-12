package main

import (
	"net/http"
)

func adminAddEventExtraLocationHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		eventID, ok := intPathValueOr400(w, r, "id", "invalid event id")
		if !ok {
			return
		}
		locationID, ok := intPathValueOr400(w, r, "location_id", "invalid location_id")
		if !ok {
			return
		}
		if err := client.AddEventExtraLocation(r.Context(), eventID, locationID, getSessionToken(r)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func adminRemoveEventExtraLocationHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		eventID, ok := intPathValueOr400(w, r, "id", "invalid event id")
		if !ok {
			return
		}
		locationID, ok := intPathValueOr400(w, r, "location_id", "invalid location_id")
		if !ok {
			return
		}
		if err := client.RemoveEventExtraLocation(r.Context(), eventID, locationID, getSessionToken(r)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func adminSetEventExtraLocationPrimaryHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		eventID, ok := intPathValueOr400(w, r, "id", "invalid event id")
		if !ok {
			return
		}
		locationID, ok := intPathValueOr400(w, r, "location_id", "invalid location_id")
		if !ok {
			return
		}
		if err := client.SetEventExtraLocationPrimary(r.Context(), eventID, locationID, getSessionToken(r)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
