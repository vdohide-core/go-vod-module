package server

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

var (
	historyMutex sync.Mutex
	csvFilename  = "metrics_history.csv"
)

// MetricRecord represents a single point in time metrics
type MetricRecord struct {
	Timestamp string  `json:"timestamp"`
	Requests  int64   `json:"requests"`
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	CacheSize int     `json:"cache_size"`
	ProcRAM   uint64  `json:"proc_ram"`
	SysCPU    float64 `json:"sys_cpu"`
	SysRAM    float64 `json:"sys_ram"`
	SysDisk   float64 `json:"sys_disk"`
}

// StartMetricCollector starts a background goroutine to log metrics to CSV every 10 seconds
func StartMetricCollector(mediaRoot string) {
	// Log immediately on startup
	recordMetric(mediaRoot)

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			recordMetric(mediaRoot)
		}
	}()
}

func recordMetric(mediaRoot string) {
	historyMutex.Lock()
	defer historyMutex.Unlock()

	// 1. Get process stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	procRAM := m.Alloc

	// 2. Get system stats
	sysCPUVal := 0.0
	cpuPercent, err := cpu.Percent(0, false)
	if err == nil && len(cpuPercent) > 0 {
		sysCPUVal = cpuPercent[0]
	}

	sysRAMVal := 0.0
	vMem, err := mem.VirtualMemory()
	if err == nil {
		sysRAMVal = vMem.UsedPercent
	}

	sysDiskVal := 0.0
	diskPath := "/"
	if runtime.GOOS == "windows" {
		diskPath = "C:"
	}
	if mediaRoot != "" {
		absRoot, err := filepath.Abs(mediaRoot)
		if err == nil {
			vol := filepath.VolumeName(absRoot)
			if vol != "" {
				diskPath = vol
			} else {
				diskPath = "/"
			}
		}
	}
	usage, err := disk.Usage(diskPath)
	if err == nil {
		sysDiskVal = usage.UsedPercent
	}

	now := time.Now().Format(time.RFC3339)
	reqs := atomic.LoadInt64(&TotalRequests)
	hits := atomic.LoadInt64(&TotalHits)
	misses := atomic.LoadInt64(&TotalMisses)
	cacheSize := 0
	if metadataCache != nil {
		cacheSize = len(metadataCache.Keys())
	}

	// 3. Append to CSV file
	file, err := os.OpenFile(csvFilename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	writer.Write([]string{
		now,
		strconv.FormatInt(reqs, 10),
		strconv.FormatInt(hits, 10),
		strconv.FormatInt(misses, 10),
		strconv.Itoa(cacheSize),
		strconv.FormatUint(procRAM, 10),
		fmt.Sprintf("%.2f", sysCPUVal),
		fmt.Sprintf("%.2f", sysRAMVal),
		fmt.Sprintf("%.2f", sysDiskVal),
	})

	// 4. Clean up older data than 7 days
	trimOldData(7 * 24 * time.Hour)
}

func trimOldData(maxAge time.Duration) {
	file, err := os.Open(csvFilename)
	if err != nil {
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-maxAge)
	var validRecords [][]string

	for _, record := range records {
		if len(record) < 9 {
			continue
		}
		t, err := time.Parse(time.RFC3339, record[0])
		if err != nil {
			// keep headers or invalid timestamps to be safe
			validRecords = append(validRecords, record)
			continue
		}
		if t.After(cutoff) {
			validRecords = append(validRecords, record)
		}
	}

	// Write back
	outFile, err := os.OpenFile(csvFilename, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return
	}
	defer outFile.Close()

	writer := csv.NewWriter(outFile)
	defer writer.Flush()
	writer.WriteAll(validRecords)
}

// ReadHistory reads all records from CSV and returns them as MetricRecord slice
func ReadHistory() ([]MetricRecord, error) {
	historyMutex.Lock()
	defer historyMutex.Unlock()

	file, err := os.Open(csvFilename)
	if err != nil {
		if os.IsNotExist(err) {
			return []MetricRecord{}, nil
		}
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	var records []MetricRecord

	for {
		line, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(line) < 9 {
			continue
		}

		reqs, _ := strconv.ParseInt(line[1], 10, 64)
		hits, _ := strconv.ParseInt(line[2], 10, 64)
		misses, _ := strconv.ParseInt(line[3], 10, 64)
		cacheSize, _ := strconv.Atoi(line[4])
		procRAM, _ := strconv.ParseUint(line[5], 10, 64)
		sysCPU, _ := strconv.ParseFloat(line[6], 64)
		sysRAM, _ := strconv.ParseFloat(line[7], 64)
		sysDisk, _ := strconv.ParseFloat(line[8], 64)

		records = append(records, MetricRecord{
			Timestamp: line[0],
			Requests:  reqs,
			Hits:      hits,
			Misses:    misses,
			CacheSize: cacheSize,
			ProcRAM:   procRAM,
			SysCPU:    sysCPU,
			SysRAM:    sysRAM,
			SysDisk:   sysDisk,
		})
	}

	return records, nil
}

// BasicAuthMiddleware restricts access to username/password (admin/admin)
func BasicAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "admin" || password != "admin" {
			w.Header().Set("WWW-Authenticate", `Basic realm="VOD Server Status Dashboard"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
