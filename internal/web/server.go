package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/skalluru/velocix/internal/manager"
)

//go:embed static/*
var staticFiles embed.FS

func NewServer(mgr *manager.Manager, port int, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()

	staticFS, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /", handleIndex())
	mux.HandleFunc("GET /api/runs", handleRuns(mgr))
	mux.HandleFunc("GET /api/stats", handleStats(mgr))
	mux.HandleFunc("GET /api/filters", handleFilters(mgr))
	mux.HandleFunc("GET /api/orgs", handleListOrgs(mgr))
	mux.HandleFunc("POST /api/orgs/active", handleSetActiveOrg(mgr))

	// Sync mode + rate limit
	mux.HandleFunc("GET /api/sync-mode", handleGetSyncMode(mgr))
	mux.HandleFunc("POST /api/sync-mode", handleSetSyncMode(mgr))
	mux.HandleFunc("POST /api/sync", handleTriggerSync(mgr))
	mux.HandleFunc("GET /api/rate-limit", handleRateLimit(mgr))

	// YACD pipeline endpoints
	mux.HandleFunc("GET /api/pipelines", handleListPipelines(mgr, logger))
	mux.HandleFunc("GET /api/pipelines/detail", handleGetPipeline(mgr, logger))
	mux.HandleFunc("POST /api/pipelines/run", handleRunPipeline(mgr, logger))
	mux.HandleFunc("GET /api/pipelines/runs", handleListPipelineRuns(mgr))
	mux.HandleFunc("GET /api/pipelines/runs/detail", handleGetPipelineRun(mgr))
	mux.HandleFunc("GET /trigger", handleTriggerPage())

	return &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: logMiddleware(logger, mux),
	}
}

func logMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func handleIndex() http.HandlerFunc {
	data, _ := staticFiles.ReadFile("static/index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

func handleTriggerPage() http.HandlerFunc {
	data, _ := staticFiles.ReadFile("static/trigger.html")
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

func handleRuns(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := mgr.GetActiveStore()
		if st == nil {
			writeJSON(w, []any{})
			return
		}
		repo := r.URL.Query().Get("repo")
		status := r.URL.Query().Get("status")
		workflow := r.URL.Query().Get("workflow")
		runs := st.Filter(repo, status, workflow)
		writeJSON(w, runs)
	}
}

func handleStats(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := mgr.GetActiveStore()
		if st == nil {
			writeJSON(w, map[string]int{})
			return
		}
		stats := st.GetStats()
		writeJSON(w, stats)
	}
}

func handleFilters(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := mgr.GetActiveStore()
		if st == nil {
			writeJSON(w, map[string]any{"repos": []string{}, "workflows": []string{}})
			return
		}
		writeJSON(w, map[string]any{
			"repos":     st.GetRepoNames(),
			"workflows": st.GetWorkflowNames(),
		})
	}
}

func handleListOrgs(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, mgr.ListOrgs())
	}
}

func handleSetActiveOrg(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		if !mgr.SetActiveOrg(body.Name) {
			http.Error(w, `{"error":"org not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"active": mgr.GetActiveOrg()})
	}
}

// --- Sync mode + rate limit handlers ---

func handleGetSyncMode(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"mode": mgr.GetSyncMode()})
	}
}

func handleSetSyncMode(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		mgr.SetSyncMode(body.Mode)
		writeJSON(w, map[string]string{"mode": mgr.GetSyncMode()})
	}
}

func handleTriggerSync(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mgr.TriggerSyncAll(context.Background())
		writeJSON(w, map[string]string{"status": "triggered"})
	}
}

func handleRateLimit(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, mgr.GetRateLimit())
	}
}

// --- YACD Pipeline handlers ---

func handleListPipelines(mgr *manager.Manager, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scanner := mgr.GetActiveScanner()
		orgCfg := mgr.GetActiveOrgConfig()
		if scanner == nil || orgCfg == nil {
			writeJSON(w, []any{})
			return
		}
		pipelines, err := scanner.ScanOrg(context.Background(), orgCfg.Organization)
		if err != nil {
			logger.Error("failed to scan pipelines", "error", err)
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, pipelines)
	}
}

func handleGetPipeline(mgr *manager.Manager, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scanner := mgr.GetActiveScanner()
		orgCfg := mgr.GetActiveOrgConfig()
		if scanner == nil || orgCfg == nil {
			http.Error(w, `{"error":"no active org"}`, http.StatusBadRequest)
			return
		}
		repo := r.URL.Query().Get("repo")
		path := r.URL.Query().Get("path")
		if repo == "" || path == "" {
			http.Error(w, `{"error":"repo and path required"}`, http.StatusBadRequest)
			return
		}
		pipeline, err := scanner.FetchPipeline(context.Background(), orgCfg.Organization, repo, path)
		if err != nil {
			logger.Error("failed to fetch pipeline", "error", err)
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, pipeline)
	}
}

func handleRunPipeline(mgr *manager.Manager, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scanner := mgr.GetActiveScanner()
		orgCfg := mgr.GetActiveOrgConfig()
		if scanner == nil || orgCfg == nil {
			http.Error(w, `{"error":"no active org"}`, http.StatusBadRequest)
			return
		}

		var body struct {
			Repo   string            `json:"repo"`
			Path   string            `json:"path"`
			Inputs map[string]string `json:"inputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}

		pipeline, err := scanner.FetchPipeline(context.Background(), orgCfg.Organization, body.Repo, body.Path)
		if err != nil {
			logger.Error("failed to fetch pipeline for run", "error", err)
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
			return
		}

		id := mgr.Runner().Run(context.Background(), pipeline, orgCfg.Organization, body.Repo, body.Path, body.Inputs)
		writeJSON(w, map[string]string{"id": id})
	}
}

func handleListPipelineRuns(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, mgr.Runner().ListRuns())
	}
}

func handleGetPipelineRun(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
			return
		}
		run := mgr.Runner().GetRun(id)
		if run == nil {
			http.Error(w, `{"error":"run not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, run)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
	}
}
