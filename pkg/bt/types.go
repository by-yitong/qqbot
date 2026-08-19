package bt

type SystemTotalRes struct {
	// CPURealUsed float64 `json:"cpuRealUsed"`
	// MemTotal    int64   `json:"memTotal"`
	// System      string  `json:"system"`
	// MemRealUsed int64   `json:"memRealUsed"`
	// CPUNum      int64   `json:"cpuNum"`
	// MemFree     int64   `json:"memFree"`
	// Version     string  `json:"version"`
	// Time        string  `json:"time"`
	// MemCached   int64   `json:"memCached"`
	// MemBuffers  int64   `json:"memBuffers"`
	// Isuser      int64   `json:"isuser"`

	MemTotal           int64    `json:"memTotal"`
	MemFree            int64    `json:"memFree"`
	MemBuffers         int64    `json:"memBuffers"`
	MemCached          int64    `json:"memCached"`
	MemAvailable       int64    `json:"memAvailable"`
	MemShared          int64    `json:"memShared"`
	MemRealUsed        int64    `json:"memRealUsed"`
	MemNewTotalList    []string `json:"memNewTotalList"`
	MemNewRealUsedList []string `json:"memNewRealUsedList"`
	MemNewRealUsed     string   `json:"memNewRealUsed"`
	MemNewTotal        string   `json:"memNewTotal"`

	CPUNum      int64   `json:"cpuNum"`
	CPURealUsed float64 `json:"cpuRealUsed"`

	Time   string `json:"time"`
	System string `json:"system"`

	Isuser  int64  `json:"isuser"`
	Isport  bool   `json:"isport"`
	Version string `json:"version"`
}
