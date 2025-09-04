package signalsampler

import (
	"encoding/json"
	"net/http"
)

// Signal 从主探针接收的采集信号
type Signal struct {
	StartTS uint64 `json:"start_ts"` // unit: second
	EndTS   uint64 `json:"end_ts"`   // unit: second

	// 筛选条件
	ContainerId string `json:"container_id"`

	PidStr string `json:"pid"`

	// Metadata
	ApmSpanId  string `json:"span_id"`
	ApmTraceId string `json:"trace_id"`

	// Namespace     string `json:"namespace"`
	// PodName       string `json:"pod_name"`
	// ContainerName string `json:"container_name"`
}

func (ls *LogSignalServer) ExposeLog(w http.ResponseWriter, r *http.Request) {
	var signal Signal
	if err := json.NewDecoder(r.Body).Decode(&signal); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	ls.SignalChan <- signal

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(OKResult)
}
