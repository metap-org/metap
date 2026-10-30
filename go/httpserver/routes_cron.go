// Admin-gated CRUD over metap's cron job store. Same shape as routes_admin.go: every handler
// uses requireAdmin, tenant always comes from the token via state.Permissions.ScopedTenant,
// never from the request body/path. Ported from crates/metap-http/src/routes/cron.rs
// (recovered from git history, see routes_admin.go's own doc comment for why).
package httpserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap-go/cron"
)

func cronNotFound(w http.ResponseWriter) {
	writeServiceError(w, http.StatusNotFound, "cron_job_not_found", "Cron job not found.", nil)
}

func cronValidationError(w http.ResponseWriter, message string) {
	writeServiceError(w, http.StatusBadRequest, "validation_failed", message, nil)
}

type createCronJobBody struct {
	Name                string          `json:"name"`
	TriggerType         string          `json:"triggerType,omitempty"`
	TriggerConfig       json.RawMessage `json:"triggerConfig,omitempty"`
	CronExpr            *string         `json:"cronExpr,omitempty"`
	Timezone            string          `json:"timezone,omitempty"`
	TargetType          string          `json:"targetType"`
	TargetConfig        json.RawMessage `json:"targetConfig"`
	DispatchMode        string          `json:"dispatchMode,omitempty"`
	MaxAttempts         int32           `json:"maxAttempts,omitempty"`
	RetryBackoffSeconds int32           `json:"retryBackoffSeconds,omitempty"`
	EnabledSet          *bool           `json:"enabled,omitempty"`
}

func (b *createCronJobBody) applyDefaults() {
	if b.TriggerType == "" {
		b.TriggerType = string(cron.TriggerSchedule)
	}
	if b.Timezone == "" {
		b.Timezone = "UTC"
	}
	if b.DispatchMode == "" {
		b.DispatchMode = string(cron.DispatchOutbox)
	}
	if b.MaxAttempts == 0 {
		b.MaxAttempts = 1
	}
	if b.RetryBackoffSeconds == 0 {
		b.RetryBackoffSeconds = 30
	}
}
func (b *createCronJobBody) enabled() bool {
	return b.EnabledSet == nil || *b.EnabledSet
}

type onTransitionTriggerConfig struct {
	Entity string `json:"entity"`
	Action string `json:"action"`
}
type onRecordEventTriggerConfig struct {
	Entity string `json:"entity"`
	Event  string `json:"event"`
}

// validateTrigger is shared by create/update: triggerType "schedule" needs a valid cronExpr/
// timezone; "on_transition" needs triggerConfig to parse as {entity, action} with both
// non-empty; "on_record_event" needs triggerConfig as {entity, event} with event one of
// created/updated/deleted. Returns ("", true) on success, else the error message to send.
func validateTrigger(triggerType string, triggerConfig json.RawMessage, cronExpr *string, timezone string) (string, bool) {
	tt, ok := cron.ParseTriggerType(triggerType)
	if !ok {
		return "`triggerType` must be one of: schedule, on_transition, on_record_event.", false
	}
	switch tt {
	case cron.TriggerSchedule:
		if cronExpr == nil {
			return "`cronExpr` is required when `triggerType` is \"schedule\".", false
		}
		if err := cron.ValidateSchedule(*cronExpr, timezone); err != nil {
			return err.Error(), false
		}
		return "", true
	case cron.TriggerOnTransition:
		if triggerConfig == nil {
			return "`triggerConfig` ({entity, action}) is required when `triggerType` is \"on_transition\".", false
		}
		var cfg onTransitionTriggerConfig
		if err := json.Unmarshal(triggerConfig, &cfg); err != nil {
			return "`triggerConfig` must be `{ entity: string, action: string }`.", false
		}
		if strings.TrimSpace(cfg.Entity) == "" || strings.TrimSpace(cfg.Action) == "" {
			return "`triggerConfig.entity`/`triggerConfig.action` must not be empty.", false
		}
		return "", true
	case cron.TriggerOnRecordEvent:
		if triggerConfig == nil {
			return "`triggerConfig` ({entity, event}) is required when `triggerType` is \"on_record_event\".", false
		}
		var cfg onRecordEventTriggerConfig
		if err := json.Unmarshal(triggerConfig, &cfg); err != nil {
			return "`triggerConfig` must be `{ entity: string, event: string }`.", false
		}
		if strings.TrimSpace(cfg.Entity) == "" {
			return "`triggerConfig.entity` must not be empty.", false
		}
		if cfg.Event != "created" && cfg.Event != "updated" && cfg.Event != "deleted" {
			return "`triggerConfig.event` must be one of: created, updated, deleted.", false
		}
		return "", true
	}
	return "`triggerType` must be one of: schedule, on_transition, on_record_event.", false
}

type stepActivity struct {
	TargetType   string          `json:"targetType"`
	TargetConfig json.RawMessage `json:"targetConfig,omitempty"`
}

// validateWaitEventConfig: a wait_event step's own targetConfig needs entity non-empty and
// exactly one of action/event set (event one of created/updated/deleted).
func validateWaitEventConfig(index int, targetConfig json.RawMessage) (string, bool) {
	var cfg cron.WaitEventTargetConfig
	if err := json.Unmarshal(targetConfig, &cfg); err != nil {
		return "`targetConfig.steps[" + strconv.Itoa(index) + "].targetConfig` must be `{ entity: string, action?: string, event?: string }`.", false
	}
	if strings.TrimSpace(cfg.Entity) == "" {
		return "`targetConfig.steps[" + strconv.Itoa(index) + "].targetConfig.entity` must not be empty.", false
	}
	switch {
	case cfg.Action != nil && cfg.Event == nil && strings.TrimSpace(*cfg.Action) != "":
		return "", true
	case cfg.Action == nil && cfg.Event != nil && (*cfg.Event == "created" || *cfg.Event == "updated" || *cfg.Event == "deleted"):
		return "", true
	case cfg.Action == nil && cfg.Event != nil:
		return "`targetConfig.steps[" + strconv.Itoa(index) + "].targetConfig.event` must be one of: created, updated, deleted.", false
	default:
		return "`targetConfig.steps[" + strconv.Itoa(index) + "].targetConfig` must set exactly one of `action`/`event`.", false
	}
}

// validateTargetConfig: targetType "steps" needs targetConfig.steps to be a non-empty array of
// {targetType, targetConfig} activities, each one of the other five (rejecting nested "steps").
func validateTargetConfig(targetType string, targetConfig json.RawMessage) (string, bool) {
	tt, _ := cron.ParseTargetType(targetType)
	if tt == cron.TargetWaitEvent {
		return "`targetType` \"wait_event\" is only valid as a step inside a \"steps\" chain, not as a job's own targetType.", false
	}
	if tt != cron.TargetSteps {
		return "", true
	}
	var obj struct {
		Steps []json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(targetConfig, &obj); err != nil || obj.Steps == nil {
		return "`targetConfig.steps` (a non-empty array) is required when `targetType` is \"steps\".", false
	}
	if len(obj.Steps) == 0 {
		return "`targetConfig.steps` must not be empty.", false
	}
	for index, step := range obj.Steps {
		var activity stepActivity
		if err := json.Unmarshal(step, &activity); err != nil {
			return "`targetConfig.steps[" + strconv.Itoa(index) + "]` must be `{ targetType: string, targetConfig: object }`.", false
		}
		stepType, ok := cron.ParseTargetType(activity.TargetType)
		switch {
		case !ok:
			return "`targetConfig.steps[" + strconv.Itoa(index) + "].targetType` must be one of: workflow_transition, bulk_query_action, webhook, email, wait_event.", false
		case stepType == cron.TargetSteps:
			return "`targetConfig.steps[" + strconv.Itoa(index) + "].targetType` cannot be \"steps\" (chains cannot nest).", false
		case stepType == cron.TargetWaitEvent:
			if msg, ok := validateWaitEventConfig(index, activity.TargetConfig); !ok {
				return msg, false
			}
		}
	}
	return "", true
}

func createCronJob(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	var body createCronJobBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	body.applyDefaults()
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if _, ok := cron.ParseTargetType(body.TargetType); !ok {
		cronValidationError(w, "`targetType` must be one of: workflow_transition, bulk_query_action, webhook, email, steps.")
		return
	}
	if msg, ok := validateTargetConfig(body.TargetType, body.TargetConfig); !ok {
		cronValidationError(w, msg)
		return
	}
	if _, ok := cron.ParseDispatchMode(body.DispatchMode); !ok {
		cronValidationError(w, "`dispatchMode` must be one of: outbox, direct.")
		return
	}
	if body.MaxAttempts < 1 {
		cronValidationError(w, "`maxAttempts` must be at least 1.")
		return
	}
	if body.RetryBackoffSeconds < 0 {
		cronValidationError(w, "`retryBackoffSeconds` must be non-negative.")
		return
	}
	if msg, ok := validateTrigger(body.TriggerType, body.TriggerConfig, body.CronExpr, body.Timezone); !ok {
		cronValidationError(w, msg)
		return
	}

	createdBy := parseOptionalUUID(rc.UserID)
	input := cron.NewCronJob{
		Name: body.Name, TriggerType: body.TriggerType, TriggerConfig: body.TriggerConfig, CronExpr: body.CronExpr,
		Timezone: body.Timezone, TargetType: body.TargetType, TargetConfig: body.TargetConfig,
		DispatchMode: body.DispatchMode, MaxAttempts: body.MaxAttempts, RetryBackoffSeconds: body.RetryBackoffSeconds,
		Enabled: body.enabled(),
	}
	job, err := cron.CreateJob(r.Context(), state.Pool, tenantID, input, createdBy)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": job})
}

func listCronJobs(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	jobs, err := cron.ListJobs(r.Context(), state.Pool, tenantID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": jobs})
}

func getCronJob(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		cronNotFound(w)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	job, err := cron.GetJob(r.Context(), state.Pool, tenantID, id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if job == nil {
		cronNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": job})
}

type updateCronJobBody struct {
	Name                *string         `json:"name,omitempty"`
	TriggerType         *string         `json:"triggerType,omitempty"`
	TriggerConfig       json.RawMessage `json:"triggerConfig,omitempty"`
	CronExpr            *string         `json:"cronExpr,omitempty"`
	Timezone            *string         `json:"timezone,omitempty"`
	TargetType          *string         `json:"targetType,omitempty"`
	TargetConfig        json.RawMessage `json:"targetConfig,omitempty"`
	DispatchMode        *string         `json:"dispatchMode,omitempty"`
	MaxAttempts         *int32          `json:"maxAttempts,omitempty"`
	RetryBackoffSeconds *int32          `json:"retryBackoffSeconds,omitempty"`
	Enabled             *bool           `json:"enabled,omitempty"`
}

func updateCronJob(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		cronNotFound(w)
		return
	}
	var body updateCronJobBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if body.TargetType != nil {
		if _, ok := cron.ParseTargetType(*body.TargetType); !ok {
			cronValidationError(w, "`targetType` must be one of: workflow_transition, bulk_query_action, webhook, email, steps.")
			return
		}
	}
	if body.DispatchMode != nil {
		if _, ok := cron.ParseDispatchMode(*body.DispatchMode); !ok {
			cronValidationError(w, "`dispatchMode` must be one of: outbox, direct.")
			return
		}
	}
	if body.MaxAttempts != nil && *body.MaxAttempts < 1 {
		cronValidationError(w, "`maxAttempts` must be at least 1.")
		return
	}
	if body.RetryBackoffSeconds != nil && *body.RetryBackoffSeconds < 0 {
		cronValidationError(w, "`retryBackoffSeconds` must be non-negative.")
		return
	}

	// Validate the *effective* trigger (existing value if the request didn't change it) so an
	// update can't leave a job with a schedule that can't be computed, or a malformed
	// on_transition triggerConfig.
	if body.TriggerType != nil || body.TriggerConfig != nil || body.CronExpr != nil || body.Timezone != nil {
		existing, err := cron.GetJob(r.Context(), state.Pool, tenantID, id)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if existing == nil {
			cronNotFound(w)
			return
		}
		triggerType := existing.TriggerType
		if body.TriggerType != nil {
			triggerType = *body.TriggerType
		}
		triggerConfig := existing.TriggerConfig
		if body.TriggerConfig != nil {
			triggerConfig = body.TriggerConfig
		}
		cronExpr := existing.CronExpr
		if body.CronExpr != nil {
			cronExpr = body.CronExpr
		}
		timezone := existing.Timezone
		if body.Timezone != nil {
			timezone = *body.Timezone
		}
		if msg, ok := validateTrigger(triggerType, triggerConfig, cronExpr, timezone); !ok {
			cronValidationError(w, msg)
			return
		}
	}
	// Same "validate the effective value" posture as the trigger block above.
	if body.TargetType != nil || body.TargetConfig != nil {
		existing, err := cron.GetJob(r.Context(), state.Pool, tenantID, id)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if existing == nil {
			cronNotFound(w)
			return
		}
		targetType := existing.TargetType
		if body.TargetType != nil {
			targetType = *body.TargetType
		}
		targetConfig := existing.TargetConfig
		if body.TargetConfig != nil {
			targetConfig = body.TargetConfig
		}
		if msg, ok := validateTargetConfig(targetType, targetConfig); !ok {
			cronValidationError(w, msg)
			return
		}
	}

	update := cron.JobUpdate{
		Name: body.Name, TriggerType: body.TriggerType, TriggerConfig: body.TriggerConfig, CronExpr: body.CronExpr,
		Timezone: body.Timezone, TargetType: body.TargetType, TargetConfig: body.TargetConfig,
		DispatchMode: body.DispatchMode, MaxAttempts: body.MaxAttempts, RetryBackoffSeconds: body.RetryBackoffSeconds,
		Enabled: body.Enabled,
	}
	job, err := cron.UpdateJob(r.Context(), state.Pool, tenantID, id, update)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if job == nil {
		cronNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": job})
}

func deleteCronJob(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		cronNotFound(w)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if err := cron.DeleteJob(r.Context(), state.Pool, tenantID, id); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func listCronJobRuns(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		cronNotFound(w)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	limit := int64(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	runs, err := cron.ListJobRuns(r.Context(), state.Pool, tenantID, id, limit)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": runs})
}

// getWorkflowRun is step-level progress for one TargetType::Steps firing — the workflow_runs
// counterpart to listCronJobRuns's plain CronJobRun row. 404 both when the job isn't a "steps"
// job and when the cron_job_runs row belongs to a different job than jobId names in the path.
func getWorkflowRun(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	jobID, err := uuid.Parse(chi.URLParam(r, "jobId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "workflow_run_not_found", "Workflow run not found.", nil)
		return
	}
	runID, err := uuid.Parse(chi.URLParam(r, "runId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "workflow_run_not_found", "Workflow run not found.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	run, err := cron.GetWorkflowRunByCronJobRun(r.Context(), state.Pool, tenantID, runID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if run == nil || run.JobID != jobID {
		writeServiceError(w, http.StatusNotFound, "workflow_run_not_found", "Workflow run not found.", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": run})
}

func mountCron(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Get("/admin/cron-jobs", wrap(listCronJobs))
	r.Post("/admin/cron-jobs", wrap(createCronJob))
	r.Get("/admin/cron-jobs/{id}", wrap(getCronJob))
	r.Patch("/admin/cron-jobs/{id}", wrap(updateCronJob))
	r.Delete("/admin/cron-jobs/{id}", wrap(deleteCronJob))
	r.Get("/admin/cron-jobs/{id}/runs", wrap(listCronJobRuns))
	r.Get("/admin/cron-jobs/{jobId}/runs/{runId}/workflow-run", wrap(getWorkflowRun))
}
