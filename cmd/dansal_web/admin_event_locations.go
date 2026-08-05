package main

import (
	"net/http"
	"strconv"
)

func adminAddEventExtraLocationHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		locationID, err := strconv.Atoi(r.PathValue("location_id"))
		if err != nil {
			http.Error(w, "invalid location_id", http.StatusBadRequest)
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
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		locationID, err := strconv.Atoi(r.PathValue("location_id"))
		if err != nil {
			http.Error(w, "invalid location_id", http.StatusBadRequest)
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
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		locationID, err := strconv.Atoi(r.PathValue("location_id"))
		if err != nil {
			http.Error(w, "invalid location_id", http.StatusBadRequest)
			return
		}
		if err := client.SetEventExtraLocationPrimary(r.Context(), eventID, locationID, getSessionToken(r)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
