package gateway

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"conduit/sdk"
)

func (g *Gateway) handleExecuteEndpoint(w http.ResponseWriter, r *http.Request, instanceID, endpointName string) {
	// Find instance
	inst, err := g.db.GetInstance(instanceID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "Connector instance not found")
		return
	}

	// Find connector type
	conn, ok := g.registry[inst.ConnectorID]
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "Connector registry entry missing")
		return
	}

	// Find endpoint definition
	var targetEP *sdk.EndpointDefinition
	for _, ep := range conn.Endpoints() {
		if ep.Name == endpointName {
			targetEP = &ep
			break
		}
	}

	if targetEP == nil {
		writeJSONError(w, http.StatusNotFound, "Endpoint not found: "+endpointName)
		return
	}

	// Retrieve credentials (decrypted automatically in db.go)
	creds, _, err := g.db.GetCredentials(instanceID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to retrieve credentials: "+err.Error())
		return
	}

	// Read body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	// Parse query params and custom headers to inject as params
	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}

	// Execute through SDK
	// Construct the client with refresh hook
	client := sdk.NewHTTPClient(
		"http://"+g.mockHost, // baseURL so HTTPClient can resolve relative paths
		*creds,
		conn.AuthConfig(),
		g.tokenRefreshHook(instanceID, conn),
	)

	req := sdk.Request{
		Body:   bodyBytes,
		Params: params,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	log.Printf("[Gateway] Executing %s.%s for instance %s", inst.ConnectorID, endpointName, instanceID)
	
	startTime := time.Now()
	resp, err := targetEP.Execute(ctx, client, req)
	duration := time.Since(startTime)

	if err != nil {
		// Classify error type
		errType := "permanent"
		if sdk.IsTransient(err) {
			errType = "transient"
		}
		RecordRequestError(inst.ConnectorID, endpointName, errType)
		RecordRequestDuration(inst.ConnectorID, endpointName, "error", duration.Seconds())

		log.Printf("[Gateway] Execution failed: %v", err)
		writeJSONError(w, http.StatusBadGateway, "Execution failed: "+err.Error())
		return
	}

	RecordRequestDuration(inst.ConnectorID, endpointName, "success", duration.Seconds())

	// Write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(resp.Body)
}
