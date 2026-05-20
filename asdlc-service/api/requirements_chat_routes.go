package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerRequirementsChatRoutes(mux *http.ServeMux, c controllers.RequirementsChatController) {
	prefix := "/api/v1/organizations/{orgHandle}/projects/{projectName}/requirements"
	mux.HandleFunc("POST "+prefix+"/chat", c.StreamChat)
	mux.HandleFunc("POST "+prefix+"/chat/turns/{turnId}/undo", c.UndoTurn)
	// Per-file Accept / Revert against the chat-session baseline. The
	// baseline ID is established by the SSE `data-session-baseline` frame
	// on the first turn and persisted client-side under the chat blob.
	mux.HandleFunc("GET "+prefix+"/chat/baseline/{baselineId}/files/{name}", c.GetBaselineFile)
	mux.HandleFunc("POST "+prefix+"/chat/baseline/{baselineId}/files/{name}/revert", c.RevertBaselineFile)
	mux.HandleFunc("DELETE "+prefix+"/chat/baseline/{baselineId}", c.DropBaseline)
}
