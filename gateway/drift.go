package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"conduit/sdk"
)

type DriftDetector struct {
	db            *DB
	registry      map[string]sdk.Connector
	webhookSecret string
	mockHost      string
	stopChan      chan struct{}
}

func NewDriftDetector(db *DB, registry map[string]sdk.Connector, webhookSecret, mockHost string) *DriftDetector {
	return &DriftDetector{
		db:            db,
		registry:      registry,
		webhookSecret: webhookSecret,
		mockHost:      mockHost,
		stopChan:      make(chan struct{}),
	}
}

func (d *DriftDetector) Start(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ticker.C:
				d.checkDrift()
			case <-d.stopChan:
				ticker.Stop()
				return
			}
		}
	}()
}

func (d *DriftDetector) Stop() {
	close(d.stopChan)
}

func (d *DriftDetector) checkDrift() {
	instances, err := d.db.ListInstances()
	if err != nil {
		log.Printf("[Drift Detector] Failed to list instances: %v", err)
		return
	}

	for _, inst := range instances {
		if inst.BaselineSchema == "" {
			// No baseline to compare against
			continue
		}

		conn, ok := d.registry[inst.ConnectorID]
		if !ok {
			continue
		}

		log.Printf("[Drift Detector] Scanning instance %s (%s) for schema drift...", inst.ID, inst.ConnectorID)

		// Parse baseline schema
		var baselineSchema map[string]string
		if err := json.Unmarshal([]byte(inst.BaselineSchema), &baselineSchema); err != nil {
			log.Printf("[Drift Detector] Failed to parse baseline schema for %s: %v", inst.ID, err)
			continue
		}

		// Retrieve credentials
		creds, _, err := d.db.GetCredentials(inst.ID)
		if err != nil {
			log.Printf("[Drift Detector] Failed to load credentials for %s: %v", inst.ID, err)
			continue
		}

		// Perform live probe request
		client := sdk.NewHTTPClient(
			"http://"+d.mockHost,
			*creds,
			conn.AuthConfig(),
			func(ctx context.Context, token *sdk.Token) (*sdk.Token, error) {
				return nil, fmt.Errorf("token refresh not supported during drift detection")
			},
		)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var bodyBytes []byte
		
		if inst.ConnectorID == "slack" {
			req := sdk.Request{
				Body: []byte(`{"channel":"C_DRIFT_PROBE","text":"drift check"}`),
			}
			resp, err := conn.Endpoints()[0].Execute(ctx, client, req)
			if err == nil {
				bodyBytes = resp.Body
			} else {
				log.Printf("[Drift Detector] Slack execution failed: %v", err)
			}
		} else if inst.ConnectorID == "github" {
			req := sdk.Request{
				Params: map[string]string{
					"owner": "drift_owner",
					"repo":  "drift_repo",
				},
			}
			var listEP *sdk.EndpointDefinition
			for _, ep := range conn.Endpoints() {
				if ep.Name == "list_issues" {
					listEP = &ep
					break
				}
			}
			if listEP != nil {
				resp, err := listEP.Execute(ctx, client, req)
				if err == nil {
					bodyBytes = resp.Body
				} else {
					log.Printf("[Drift Detector] GitHub execution failed: %v", err)
				}
			}
		} else if inst.ConnectorID == "stripe" {
			req := sdk.Request{Params: map[string]string{"limit": "3"}}
			for _, ep := range conn.Endpoints() {
				if ep.Name == "list_charges" {
					resp, err := ep.Execute(ctx, client, req)
					if err == nil {
						bodyBytes = resp.Body
					} else {
						log.Printf("[Drift Detector] Stripe execution failed: %v", err)
					}
					break
				}
			}
		}
		cancel()

		if len(bodyBytes) == 0 {
			// Probe failed completely, possibly network error or auth failure. This is tracked by error-rate metrics, not drift.
			continue
		}

		// Unmarshal live JSON
		var liveObj interface{}
		if err := json.Unmarshal(bodyBytes, &liveObj); err != nil {
			log.Printf("[Drift Detector] Failed to unmarshal live response for %s: %v", inst.ID, err)
			continue
		}

		// Flatten live JSON
		liveSchema := make(map[string]string)
		flattenJSONSchema("", liveObj, liveSchema)

		// Compare live vs baseline
		drifted := false
		var driftDetails []string

		for key, expectedType := range baselineSchema {
			actualType, exists := liveSchema[key]
			if !exists {
				drifted = true
				driftDetails = append(driftDetails, fmt.Sprintf("Field %q removed", key))
			} else if actualType != expectedType {
				drifted = true
				driftDetails = append(driftDetails, fmt.Sprintf("Field %q type changed from %s to %s", key, expectedType, actualType))
			}
		}

		if drifted {
			log.Printf("[DRIFT DETECTED] Instance %s (%s) has drifted! Details: %s", inst.ID, inst.ConnectorID, strings.Join(driftDetails, "; "))
			inst.Status = "drifted"
			d.db.SaveInstance(inst)
			d.triggerAlert(inst, driftDetails)
			RecordDriftDetection(inst.ConnectorID, "drifted")
		} else {
			if inst.Status == "drifted" {
				log.Printf("[Drift Detector] Instance %s (%s) returned to healthy status.", inst.ID, inst.ConnectorID)
				inst.Status = "active"
				d.db.SaveInstance(inst)
			}
			RecordDriftDetection(inst.ConnectorID, "healthy")
		}
	}
}

func (d *DriftDetector) triggerAlert(inst *ConnectorInstance, details []string) {
	// Alert logging
	log.Printf("[ALERT] SCHEMA DRIFT ALERT - Connector: %s, Instance: %s, Issues: %v", inst.ConnectorID, inst.ID, details)
	
	// Simulated webhook notifier alert
	alertPayload := map[string]interface{}{
		"event":        "schema_drift",
		"connector_id": inst.ConnectorID,
		"instance_id":  inst.ID,
		"details":      details,
		"timestamp":    time.Now().Format(time.RFC3339),
	}

	payloadBytes, _ := json.Marshal(alertPayload)
	
	// Send to a simulated slack alert webhook (or standard HTTP endpoint) if configured in env
	alertURL := os.Getenv("CONDUIT_ALERT_WEBHOOK_URL")
	if alertURL != "" {
		go func() {
			resp, err := http.Post(alertURL, "application/json", io.NopCloser(strings.NewReader(string(payloadBytes))))
			if err != nil {
				log.Printf("[Alert Hook] Failed to send alert webhook: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
}

// flattenJSONSchema converts nested JSON into a flattened dot-path format.
func flattenJSONSchema(path string, val interface{}, schema map[string]string) {
	prefix := ""
	if path != "" {
		prefix = path + "."
	}

	switch v := val.(type) {
	case map[string]interface{}:
		for k, item := range v {
			flattenJSONSchema(prefix+k, item, schema)
		}
	case []interface{}:
		if len(v) == 0 {
			schema[path] = "array[empty]"
			return
		}
		// Inspect first item to extract array element schema
		first := v[0]
		switch f := first.(type) {
		case map[string]interface{}:
			// Array of objects, append "[]" to path
			flattenJSONSchema(path+".[]", f, schema)
		default:
			typeName := getPrimitiveType(f)
			schema[path] = "array[" + typeName + "]"
		}
	default:
		schema[path] = getPrimitiveType(v)
	}
}

func getPrimitiveType(v interface{}) string {
	if v == nil {
		return "null"
	}
	switch v.(type) {
	case string:
		return "string"
	case float64, int, int64, float32:
		return "number"
	case bool:
		return "boolean"
	default:
		return "unknown"
	}
}
