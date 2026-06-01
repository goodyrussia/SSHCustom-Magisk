// Package metrics reads /proc for CPU and memory usage.
package metrics

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Sample holds a point-in-time resource snapshot.
type Sample struct {
	CPUPct   float64 // CPU usage percentage (since last sample)
	RSSMB    float64 // Resident set size in MB
	SysMemMB float64 // Total system memory in MB
	SysUsedMB float64 // Used system memory in MB
	At       time.Time
}

// Sampler tracks CPU usage across samples by keeping the previous tick values.
type Sampler struct {
	prevUTime uint64
	prevSTime uint64
	prevAt    time.Time
}

// Sample reads /proc/self/stat, /proc/self/statm, /proc/meminfo and returns a
// resource snapshot. CPU% is computed as the delta since the previous Sample
// call.
func (s *Sampler) Sample() Sample {
	now := time.Now()
	samp := Sample{At: now}

	// CPU from /proc/self/stat
	if utime, stime, err := readProcStat(); err == nil {
		if !s.prevAt.IsZero() {
			elapsed := now.Sub(s.prevAt).Seconds()
			if elapsed > 0 {
				totalDelta := (utime - s.prevUTime) + (stime - s.prevSTime)
				// /proc/stat ticks are in USER_HZ (usually 100), convert to seconds
				samp.CPUPct = (float64(totalDelta) / 100.0) / elapsed * 100.0
				if samp.CPUPct > 100 {
					samp.CPUPct = 100
				}
			}
		}
		s.prevUTime = utime
		s.prevSTime = stime
		s.prevAt = now
	}

	// RSS from /proc/self/statm
	if rssPages, err := readProcStatm(); err == nil {
		// statm field 1 is RSS in pages (typically 4096 bytes)
		samp.RSSMB = float64(rssPages*4096) / (1024 * 1024)
	}

	// System memory from /proc/meminfo
	if total, used, err := readProcMeminfo(); err == nil {
		samp.SysMemMB = float64(total) / 1024
		samp.SysUsedMB = float64(used) / 1024
	}

	return samp
}

// readProcStat returns utime and stime (fields 14 and 15) from /proc/self/stat.
func readProcStat() (utime, stime uint64, err error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, err
	}
	// The stat file format has the comm field in parentheses which may contain
	// spaces. Find the closing paren.
	s := string(data)
	closeParen := strings.LastIndex(s, ")")
	if closeParen < 0 {
		return 0, 0, fmt.Errorf("malformed /proc/self/stat")
	}
	fields := strings.Fields(s[closeParen+2:])
	// After ")": state, ppid, pgrp, session, tty_nr, tpgid, flags, minflt,
	// cminflt, majflt, cmajflt, utime(13), stime(14)
	if len(fields) < 15 {
		return 0, 0, fmt.Errorf("not enough fields in /proc/self/stat")
	}
	utime, err = strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	stime, err = strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return utime, stime, nil
}

// readProcStatm returns the RSS field (index 1) in pages.
func readProcStatm() (rssPages uint64, err error) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, fmt.Errorf("not enough fields in /proc/self/statm")
	}
	rssPages, err = strconv.ParseUint(fields[1], 10, 64)
	return rssPages, err
}

// readProcMeminfo returns total and used memory in KB.
func readProcMeminfo() (totalKB, usedKB uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	var memTotal, memFree, buffers, cached uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.TrimSpace(parts[1])
		valStr = strings.TrimSuffix(valStr, " kB")
		valStr = strings.TrimSpace(valStr)
		val, err := strconv.ParseUint(valStr, 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			memTotal = val
		case "MemFree":
			memFree = val
		case "Buffers":
			buffers = val
		case "Cached":
			cached = val
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if memTotal == 0 {
		return 0, 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
	}
	usedKB = memTotal - memFree - buffers - cached
	return memTotal, usedKB, nil
}
