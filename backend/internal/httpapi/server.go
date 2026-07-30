package httpapi

import (
	"encoding/json"
	"net/http"

	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

type server struct {
	store *dorasqlite.Store
}

type healthResponse struct {
	Backend       bool   `json:"backend"`
	SQLite        bool   `json:"sqlite"`
	InitializedAt string `json:"initializedAt"`
}

func NewHandler(store *dorasqlite.Store) http.Handler {
	s := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.health)
	return mux
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	initializedAt, err := s.store.InitializedAt(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, healthResponse{Backend: true})
		return
	}

	writeJSON(w, healthResponse{
		Backend:       true,
		SQLite:        true,
		InitializedAt: initializedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	_ = json.NewEncoder(w).Encode(value)
}
