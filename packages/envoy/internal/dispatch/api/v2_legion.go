package api

// v2_legion.go is a project's Legion daemon and board under /api/v2: the four routes #2731 added
// to /api/v1 before /api/v2's change merged (testdata/v1-only-routes.txt), each at its /api/v1
// path with the two segments legion/<name> written as one compound noun, as
// /api/v1/ci-reconcile/passes is /api/v2/ci-reconcile-passes. The daemon a project's state is read
// from is the singleton /api/v2/projects/{key}/legion-daemon, which a PUT names and a DELETE
// removes, and the board read from what the sampler recorded of it is the report
// /api/v2/projects/{key}/legion-board. Each runs the work its /api/v1 route runs
// (answerLegionSetting, writeLegionSetting, removeLegionSetting, answerLegionBoard), the project
// checked first and answered 404 PROJECT_NOT_FOUND (dispatch://INFRA-4071/spec R7).

import (
	"errors"
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
	"github.com/sjawhar/envoy/internal/dispatch/legion"
)

// legionUnconfiguredRefusal is a project that names no Legion daemon, which the board read and
// the daemon's removal answer with one code.
var legionUnconfiguredRefusal = apicode.Refusal{Condition: apicode.LegionUnconfigured, Code: apicode.LegionNotConfigured}

// legionSettingText is what the daemon routes' descriptions say of the setting they answer.
const legionSettingText = "{project, state_url, created_by, updated_at}: the base URL Dispatch reads the daemon's " +
	"GET /legion/v1/state from once a minute, the actor that first named a daemon of the project, and when the " +
	"setting was last written"

// v2LegionRows are a project's Legion daemon and board, #2731's four rows of /api/v1.
func (s *server) v2LegionRows() []routeRow {
	return []routeRow{
		{
			Method: http.MethodGet, Pattern: "/projects/{key}/legion-board", Auth: authAny, Versions: onlyV2,
			Description: "The Legion board of the project its path names, from what the sampler recorded of that " +
				"project's daemon: {project, header, columns, settings, computed_at}. columns are the eleven columns " +
				"(queue, design, planning, implementing, testing, reviewing, accepting, retro, merging, awaiting_person, " +
				"held) in that order, each card naming its issue's key, title, priority, phase and when that phase " +
				"began, the slot it holds, the roles in a turn now, the issues a blocker link holds it on, what it waits " +
				"on from a person, its tree root and the children of its tree that have not started; header holds the " +
				"slots in use, the slot cap, the queue's length and the last read of the daemon with its failure; " +
				"settings are the settings the daemon reports, {} until a read records them. A project whose daemon has " +
				"never answered is answered the board with empty columns. The live socket's legion view sends the same " +
				"body. 404 LEGION_NOT_CONFIGURED when the project names no daemon, 404 PROJECT_NOT_FOUND for a project " +
				"that does not exist.",
			Handler:  s.getLegionBoardV2,
			Resource: "legion-board", Kind: kindReport,
			Shape:    routeShape{Status: http.StatusOK, Response: legionBoardResponse{}},
			Refusals: []apicode.Refusal{undeclaredKeyRefusal, projectMissingRefusal, legionUnconfiguredRefusal},
		},
		{
			Method: http.MethodGet, Pattern: "/projects/{key}/legion-daemon", Auth: authAny, Versions: onlyV2,
			Description: "The Legion daemon this project's state is read from, " + legionSettingText + "; null when " +
				"the project names none. No route lists every project's daemon: a page that shows them all reads each " +
				"project's. 404 PROJECT_NOT_FOUND for a project that does not exist.",
			Handler:  s.getLegionDaemonV2,
			Resource: "legion-daemon", Kind: kindSingleton,
			Shape: routeShape{
				Status: http.StatusOK, Response: legion.Settings{},
				// A project that names no daemon answers 200 and the JSON literal `null`
				// (answerLegionSetting), as the /api/v1 read does.
				Nullable: true,
			},
			Refusals: []apicode.Refusal{undeclaredKeyRefusal, projectMissingRefusal},
		},
		{
			Method: http.MethodPut, Pattern: "/projects/{key}/legion-daemon", Auth: authAny, Versions: onlyV2,
			Description: "Name the Legion daemon this project's state is read from {state_url}, a bearer naming its " +
				"session in actor: an absolute http(s) URL with a host and no path, carrying no user, password, query or " +
				"fragment, else 400 LEGION_SETTING_INPUT. The daemon belongs to the deployment, so a credential scoped " +
				"to one project is refused it, the project this path names included. The setting records no setter, so " +
				"a PUT of the state_url it already holds, by any caller, answers the setting as it stands and writes " +
				"nothing. Answers 200 the setting, " + legionSettingText + ", and an open board of the project hears of " +
				"the write at once. 404 PROJECT_NOT_FOUND for a project that does not exist.",
			Handler:  s.putLegionDaemonV2,
			Resource: "legion-daemon", Kind: kindSingleton,
			Body:  requestBody{Kind: bodyJSON, Type: putLegionSettingInput{}},
			Shape: routeShape{Status: http.StatusOK, Response: legion.Settings{}},
			// Pointing Dispatch at a host is the deployment's write (settings_legion.go).
			Scope:    scopeDeployment,
			Refusals: []apicode.Refusal{undeclaredKeyRefusal, projectMissingRefusal},
		},
		{
			Method: http.MethodDelete, Pattern: "/projects/{key}/legion-daemon", Auth: authAny, Versions: onlyV2,
			Description: "Stop reading this project's Legion daemon, a bearer naming its session in actor; what was " +
				"already read stays, and nothing reads it again. Scoped as the PUT is. 204. 404 LEGION_NOT_CONFIGURED " +
				"when the project names no daemon, 404 PROJECT_NOT_FOUND for a project that does not exist.",
			Handler:  s.deleteLegionDaemonV2,
			Resource: "legion-daemon", Kind: kindSingleton,
			Body:     requestBody{Kind: bodyJSON, Type: actorInput{}, Optional: true},
			Shape:    routeShape{Status: http.StatusNoContent},
			Scope:    scopeDeployment,
			Refusals: []apicode.Refusal{undeclaredKeyRefusal, projectMissingRefusal, legionUnconfiguredRefusal},
		},
	}
}

// getLegionBoardV2 answers GET /api/v2/projects/{key}/legion-board: the board GET
// /api/v1/projects/{key}/legion/board answers (answerLegionBoard), the project held to exist
// first.
func (s *server) getLegionBoardV2(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !s.requireAuthenticated(w, r) || !s.requireProjectV2(w, r, key) {
		return
	}
	s.answerLegionBoard(w, r, key)
}

// getLegionDaemonV2 answers GET /api/v2/projects/{key}/legion-daemon: the setting GET
// /api/v1/projects/{key}/legion/daemon answers (answerLegionSetting), the project held to exist
// first.
func (s *server) getLegionDaemonV2(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !s.requireAuthenticated(w, r) || !s.requireProjectV2(w, r, key) {
		return
	}
	s.answerLegionSetting(w, r, key)
}

// putLegionDaemonV2 answers PUT /api/v2/projects/{key}/legion-daemon: the write PUT
// /api/v1/projects/{key}/legion/daemon makes (writeLegionSetting), once the no-op rule of
// dispatch://INFRA-4071/spec R8 has found the body is a change. The setting's created_by is the
// actor that first named a daemon of the project, which a later write keeps, so it records no
// setter of the state_url it holds, and a body naming that state_url answers the setting and
// writes nothing whoever sends it (noSetterRecorded): no updated_at moves and no board is told.
func (s *server) putLegionDaemonV2(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !s.requireAuthenticated(w, r) || !s.requireProjectV2(w, r, key) {
		return
	}
	body := v2Body[putLegionSettingInput](r)
	actor, ok := s.requireActor(w, r, body.Actor)
	if !ok {
		return
	}
	stateURL, refusal := legionStateURL(body.StateURL)
	if refusal != "" {
		writeError(w, r, apicode.LegionSettingInput, refusal)
		return
	}
	stored, err := legion.GetSettings(r.Context(), s.deps.Store.Pool, key)
	switch {
	case err == nil:
		sent := putLegionSettingInput{StateURL: stateURL, Actor: body.Actor}
		if putChangesNothing(sent, putLegionSettingInput{StateURL: stored.StateURL, Actor: body.Actor}, noSetterRecorded, "") {
			WriteJSON(w, http.StatusOK, stored)
			return
		}
	case !errors.Is(err, legion.ErrNoSettings):
		s.writeHandlerError(w, r, err)
		return
	}
	s.writeLegionSetting(w, r, key, stateURL, actor)
}

// deleteLegionDaemonV2 answers DELETE /api/v2/projects/{key}/legion-daemon: the removal DELETE
// /api/v1/projects/{key}/legion/daemon makes (removeLegionSetting), a bearer naming its session in
// actor, a project that names no daemon answered LEGION_NOT_CONFIGURED as its board read is.
func (s *server) deleteLegionDaemonV2(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !s.requireAuthenticated(w, r) || !s.requireProjectV2(w, r, key) {
		return
	}
	if _, ok := s.requireActor(w, r, v2Body[actorInput](r).Actor); !ok {
		return
	}
	s.removeLegionSetting(w, r, key, apicode.LegionNotConfigured)
}
